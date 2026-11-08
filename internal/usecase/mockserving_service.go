package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// MockServingService is the central business scenario of the application:
// given a request (projectID, method, path, scenario), build the response
// using the project's active contract and configured mock rules, falling
// back to an example generated directly from the contract's JSON Schema.
// It knows nothing about net/http — it decides only "what to respond" and
// not "how to write it to the socket.".
type MockServingService struct {
	contracts ContractRepository
	mocks     MockRepository
	logs      LogRepository
	engine    ContractEngine
	cfg       servingConfig
	templates *TemplateEngine

	// recordMu serializes recording, so concurrent identical requests save
	// one rule.
	recordMu sync.Mutex
	// driftSem bounds the drift checks running at once; driftWG lets WaitDrift
	// wait for them.
	driftSem chan struct{}
	driftWG  sync.WaitGroup
}

// MockRequest is an incoming mock-server request. Query, Header and Body feed
// request validation; they may be left empty when SkipValidation is set.
type MockRequest struct {
	Method   string
	Path     string
	Scenario string
	Query    map[string][]string
	Header   map[string][]string
	Body     []byte
	// SkipValidation serves the request without checking it against the
	// contract, whatever the project's validation mode is.
	SkipValidation bool
	// Context cancels a request forwarded upstream when the client goes away.
	// Nil means context.Background().
	Context context.Context //nolint:containedctx // a request value, like http.Request's.
}

func NewMockServingService(
	contracts ContractRepository,
	mocks MockRepository,
	logs LogRepository,
	engine ContractEngine,
	opts ...ServingOption,
) *MockServingService {
	return &MockServingService{
		contracts: contracts,
		mocks:     mocks,
		logs:      logs,
		engine:    engine,
		cfg:       newServingConfig(opts),
		templates: NewTemplateEngine(),
		driftSem:  make(chan struct{}, maxParallelDrifts),
	}
}

// Serve returns a ready MockResponse. An error is returned only for truly
// exceptional situations — for ordinary "endpoint not described in the
// contract" it returns a MockResponse with status 404, so the calling code
// (the HTTP adapter) doesn't have to duplicate error mapping and can just
// write the response as-is, logging it.
//
// Serve never validates the request; use ServeRequest for that.
func (s *MockServingService) Serve(projectID, method, path, scenario string) MockResponse {
	return s.ServeRequest(projectID, &MockRequest{
		Method: method, Path: path, Scenario: scenario, SkipValidation: true,
	})
}

// ServeRequest is like Serve, but also validates the request against the
// contract according to the project's validation mode (see
// domain.ValidationMode): "warn" serves the mock and records the violations,
// "enforce" answers 400 with the violations instead of the mock.
func (s *MockServingService) ServeRequest(projectID string, req *MockRequest) MockResponse {
	method, path := req.Method, req.Path
	sv := &served{req: req, endpoint: "", start: time.Now()}

	if resp, ok := s.cfg.auth.HandleToken(projectID, req); ok {
		return s.finish(projectID, sv, resp)
	}

	contract, err := s.contracts.GetActive(projectID)
	if err != nil {
		return s.finish(projectID, sv, &MockResponse{
			StatusCode: StatusNotFound, ContentType: ContentTypeJSON,
			Body: errorBody("no contract has been uploaded for this project yet"),
		})
	}

	pathTemplate, _, found := s.engine.FindOperation([]byte(contract.Raw), method, path)
	if !found {
		return s.finish(projectID, sv, &MockResponse{
			StatusCode: StatusNotFound, ContentType: ContentTypeJSON,
			Body: errorBody(fmt.Sprintf("endpoint not described in contract: %s %s", method, path)),
		})
	}

	sv.endpoint = pathTemplate

	if denied := s.cfg.auth.Authorize(projectID, contract.Raw, req); denied != nil {
		return s.finish(projectID, sv, denied)
	}

	violations := s.validate(projectID, contract.Raw, req)
	if len(violations) > 0 && s.cfg.validationMode(projectID) == domain.ValidationEnforce {
		return s.finish(projectID, sv, rejectInvalidRequest(violations))
	}

	resp := s.respond(projectID, contract.Raw, pathTemplate, req)
	if len(violations) > 0 {
		resp = withWarnings(resp, violations)
	}

	s.startDrift(projectID, contract.Raw, req, resp)

	return s.finish(projectID, sv, resp)
}

// validate returns the request's contract violations, or nil when validation
// is off (or skipped) for the project.
func (s *MockServingService) validate(projectID, raw string, req *MockRequest) []string {
	if req.SkipValidation || s.cfg.validationMode(projectID) == domain.ValidationOff {
		return nil
	}

	return s.engine.ValidateRequest([]byte(raw), &RequestData{
		Method: req.Method, Path: req.Path, Query: req.Query, Header: req.Header, Body: req.Body,
	})
}

// rejectInvalidRequest builds the 400 response of enforce mode.
func rejectInvalidRequest(violations []string) *MockResponse {
	body, err := json.Marshal(map[string]any{
		"error":      "request does not match the contract",
		"violations": violations,
	})
	if err != nil {
		body = errorBody("request does not match the contract")
	}

	return &MockResponse{
		StatusCode: StatusBadRequest, ContentType: ContentTypeJSON, Body: body,
		Source: "request-validation", Violations: violations,
	}
}

// withWarnings annotates a served response with the violations of warn mode.
// The header map is copied so a mock rule's own headers are never modified.
func withWarnings(resp *MockResponse, violations []string) *MockResponse {
	headers := make(map[string]string, len(resp.Headers)+1)
	for k, v := range resp.Headers {
		headers[k] = v
	}

	headers["X-Mock-Validation"] = fmt.Sprintf("warn: %d violation(s)", len(violations))
	resp.Headers = headers
	resp.Violations = violations

	return resp
}

// respond picks the mock rule for the operation, or builds an example from
// the contract schema when no rule matches.
func (s *MockServingService) respond(projectID, raw, pathTemplate string, req *MockRequest) *MockResponse {
	method, path, scenario := req.Method, req.Path, req.Scenario

	tc := s.templateContext(projectID, pathTemplate, req)

	if rule := s.findRule(projectID, pathTemplate, method, scenario, tc); rule != nil {
		return s.fromRule(rule, tc)
	}

	if !IsWebSocketUpgrade(tc.Header) && s.hasWebSocketRule(projectID, pathTemplate, method) {
		return upgradeRequired(SourceRule)
	}

	if resp, ok := s.proxy(projectID, pathTemplate, req, tc); ok {
		return resp
	}

	if s.cfg.state != nil {
		resp, ok := s.cfg.state.Handle(projectID, raw, pathTemplate, req)
		if ok {
			return resp
		}
	}

	if resp, ok := s.fromContractScript(raw, method, path, tc); ok {
		return resp
	}

	// Fallback: no rule configured manually or via LLM — example from schema, 200.
	status := "200"

	body, contentType, err := s.engine.ExampleResponse([]byte(raw), method, path, status)
	if err != nil {
		return &MockResponse{
			StatusCode: StatusInternalServerError, ContentType: ContentTypeJSON,
			Body: errorBody("failed to build response example: " + err.Error()),
		}
	}

	code, _ := strconv.Atoi(status)

	if strings.HasPrefix(strings.ToLower(contentType), ContentTypeEventStream) {
		plan := s.planStream(scriptFromEventStream(body), tc)

		return streamResponse(plan, 0, SourceSchemaExample, "")
	}

	return &MockResponse{
		StatusCode: code, ContentType: nonEmpty(contentType, ContentTypeJSON),
		Body: body, Source: SourceSchemaExample, Matched: true,
	}
}

// findRule picks the mock rule for the operation. An explicit scenario wins;
// otherwise the rule with the most conditions that all match the request;
// otherwise the rule recorded for exactly this request; otherwise the
// unconditional default rule.
func (s *MockServingService) findRule(
	projectID, pathTemplate, method, scenario string, tc *TemplateContext,
) *domain.MockRule {
	var rules []*domain.MockRule

	upgrade := IsWebSocketUpgrade(tc.Header)

	for _, m := range s.mocks.ListMocks(projectID) {
		// A WebSocket rule answers upgrade requests, and only those.
		if strings.EqualFold(m.Method, method) && m.Path == pathTemplate && (m.WebSocket != nil) == upgrade {
			rules = append(rules, m)
		}
	}

	if rule := explicitRule(rules, scenario); rule != nil {
		return rule
	}

	if best := bestConditionalRule(rules, tc); best != nil {
		return best
	}

	if rec := recordedRule(rules, tc); rec != nil {
		return rec
	}

	return defaultRule(rules)
}

// explicitRule returns the rule a client asked for with X-Mock-Scenario. The
// scenario "default" is not a way to bypass conditions: it selects the
// unconditional default rule, never a conditional rule that merely carries
// the "default" scenario name (which is what the UI saves when the scenario
// field is left empty).
func explicitRule(rules []*domain.MockRule, scenario string) *domain.MockRule {
	switch scenario {
	case "":
		return nil
	case ScenarioDefault:
		return defaultRule(rules)
	case ScenarioRecorded:
		return nil // recorded rules answer the request they were recorded from, not a name.
	}

	for _, m := range rules {
		if m.Scenario == scenario {
			return m
		}
	}

	return nil
}

// bestConditionalRule returns the rule whose conditions all match and that has
// the most of them (the first one on a tie).
func bestConditionalRule(rules []*domain.MockRule, tc *TemplateContext) *domain.MockRule {
	var best *domain.MockRule

	for _, m := range rules {
		if len(m.Match) > 0 && (best == nil || len(m.Match) > len(best.Match)) && tc.matchesAll(m.Match) {
			best = m
		}
	}

	return best
}

// defaultRule returns the last unconditional rule without a named scenario.
func defaultRule(rules []*domain.MockRule) *domain.MockRule {
	var fallback *domain.MockRule

	for _, m := range rules {
		if len(m.Match) == 0 && (m.Scenario == "" || m.Scenario == ScenarioDefault) {
			fallback = m
		}
	}

	return fallback
}

// templateContext describes the request for {{placeholder}} rendering.
func (s *MockServingService) templateContext(projectID, pathTemplate string, req *MockRequest) *TemplateContext {
	return &TemplateContext{
		ProjectID: projectID, Method: strings.ToUpper(req.Method), Path: req.Path,
		PathParams: pathParams(pathTemplate, req.Path),
		Query:      req.Query, Header: req.Header, Body: req.Body,
		JSON: false, body: nil, parsed: false,
	}
}

// pathParams maps the {name} segments of a path template to the matching
// segments of the request path.
func pathParams(pathTemplate, path string) map[string]string {
	params := map[string]string{}
	tpl := strings.Split(strings.Trim(pathTemplate, "/"), "/")
	act := strings.Split(strings.Trim(path, "/"), "/")

	for i, seg := range tpl {
		if i < len(act) && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			params[seg[1:len(seg)-1]] = act[i]
		}
	}

	return params
}

func (s *MockServingService) fromRule(rule *domain.MockRule, tc *TemplateContext) *MockResponse {
	settings := settingsOf(rule)
	delay := settings.latencyMs()
	fault := rollFault(&settings)

	// Chaos testing: some faults replace the rule's response altogether,
	// even when the rule describes a successful scenario.
	if resp := faultResponse(rule, &settings, fault, delay); resp != nil {
		return resp
	}

	code := rule.StatusCode
	if code == 0 {
		code = 200
	}

	contentType := nonEmpty(rule.ContentType, ContentTypeJSON)

	headers, body := rule.Headers, rule.Body
	if !rule.Raw {
		headers, body = s.renderHeaders(headers, tc), s.renderBody(body, contentType, tc)
	}

	if rule.WebSocket != nil {
		return websocketResponse(s.planWebSocket(rule.WebSocket, tc), delay, SourceRule, rule.ID)
	}

	if rule.Stream != nil {
		resp := streamResponse(s.planStream(rule.Stream, tc), delay, SourceRule, rule.ID)
		resp.StatusCode, resp.Headers = code, mergeHeaders(resp.Headers, headers)

		return resp
	}

	resp := &MockResponse{
		StatusCode: code, ContentType: contentType, Headers: headers, Body: []byte(body),
		DelayMs: delay, Source: SourceRule, MatchedRule: rule.ID, Matched: true,
	}

	if fault != "" {
		breakBody(resp, fault)
	}

	return resp
}

// renderBody fills the {{placeholders}} of a rule body.
func (s *MockServingService) renderBody(body, contentType string, tc *TemplateContext) string {
	ctx := *tc
	ctx.JSON = strings.Contains(strings.ToLower(contentType), "json")

	return s.templates.Render(body, &ctx)
}

// renderHeaders fills the {{placeholders}} of rule header values (as text).
func (s *MockServingService) renderHeaders(headers map[string]string, tc *TemplateContext) map[string]string {
	if len(headers) == 0 {
		return headers
	}

	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = s.templates.Render(v, tc)
	}

	return out
}

// served is what finish needs to know about the request being answered.
type served struct {
	req *MockRequest
	// endpoint is the contract path template, once the operation is known.
	endpoint string
	start    time.Time
}

func (s *MockServingService) finish(projectID string, sv *served, resp *MockResponse) MockResponse {
	// The duration is what the client sees: the time spent here plus the
	// latency the HTTP adapter adds afterwards.
	took := time.Since(sv.start).Milliseconds() + int64(resp.DelayMs)

	s.logs.Add(&domain.RequestLog{
		ID: "", ProjectID: projectID, Method: sv.req.Method, Path: sv.req.Path,
		StatusCode: resp.StatusCode, MatchedRule: resp.MatchedRule, Matched: resp.Matched,
		Timestamp: time.Now().UTC(), DurationMs: took,
		Violations: resp.Violations, Fault: resp.Fault, Source: resp.Source,
		Endpoint: sv.endpoint, Captured: false,
		Request:  rawCapture(sv.req.Header, sv.req.Body),
		Response: responseCapture(resp.ContentType, resp.Headers, resp.Body),
	})

	return *resp
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}

	return s
}

func errorBody(msg string) []byte {
	// equivalent to json.Marshal(map[string]string{"error": msg}), but without
	// importing encoding/json just for one string — manually escape the minimum needed.
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(msg)
	return []byte(`{"error":"` + esc + `"}`)
}

// IsWebSocketUpgrade reports whether the headers ask for a WebSocket upgrade.
func IsWebSocketUpgrade(h map[string][]string) bool {
	return headerHasToken(h, headerUpgrade, "websocket") && headerHasToken(h, headerConnection, "upgrade")
}

// headerHasToken reports whether a comma-separated header holds the token.
func headerHasToken(h map[string][]string, name, token string) bool {
	for k, values := range h {
		if !strings.EqualFold(k, name) {
			continue
		}

		for _, v := range values {
			for _, part := range strings.Split(v, ",") {
				if strings.EqualFold(strings.TrimSpace(part), token) {
					return true
				}
			}
		}
	}

	return false
}

func mergeHeaders(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}

	for k, v := range extra {
		out[k] = v
	}

	return out
}

// fromContractScript answers with the stream or WebSocket script declared on
// the operation in the contract itself (x-mock-stream / x-mock-websocket).
func (s *MockServingService) fromContractScript(raw, method, path string, tc *TemplateContext) (*MockResponse, bool) {
	scripter, ok := s.engine.(StreamScripter)
	if !ok {
		return nil, false
	}

	stream, ws, err := scripter.StreamScripts([]byte(raw), method, path)
	if err != nil {
		return nil, false
	}

	if IsWebSocketUpgrade(tc.Header) {
		if ws != nil {
			return websocketResponse(s.planWebSocket(ws, tc), 0, SourceContract, ""), true
		}

		return nil, false
	}

	if stream != nil {
		return streamResponse(s.planStream(stream, tc), 0, SourceContract, ""), true
	}

	if ws != nil {
		return upgradeRequired(SourceContract), true
	}

	return nil, false
}

// upgradeRequired answers a plain HTTP request to a WebSocket-only operation.
func upgradeRequired(source string) *MockResponse {
	return &MockResponse{
		StatusCode: StatusUpgradeRequired, ContentType: ContentTypeJSON,
		Headers: map[string]string{headerUpgrade: "websocket", headerConnection: headerUpgrade},
		Body:    errorBody("this operation speaks WebSocket; send an Upgrade: websocket request"),
		Source:  source, Matched: true,
	}
}

// hasWebSocketRule reports whether the operation has a WebSocket mock rule.
func (s *MockServingService) hasWebSocketRule(projectID, pathTemplate, method string) bool {
	for _, m := range s.mocks.ListMocks(projectID) {
		if m.WebSocket != nil && strings.EqualFold(m.Method, method) && m.Path == pathTemplate {
			return true
		}
	}

	return false
}

// ServeRPC answers a call of a non-HTTP protocol (gRPC) that is described by
// its own contract engine: the mock rule for the endpoint decides, otherwise
// fallback builds the default JSON body. The call is logged like any request.
func (s *MockServingService) ServeRPC(
	projectID, endpoint string, req *MockRequest, fallback func() ([]byte, error),
) MockResponse {
	sv := &served{req: req, endpoint: endpoint, start: time.Now()}
	tc := s.templateContext(projectID, endpoint, req)

	if rule := s.findRule(projectID, endpoint, req.Method, req.Scenario, tc); rule != nil {
		return s.finish(projectID, sv, s.fromRule(rule, tc))
	}

	body, err := fallback()
	if err != nil {
		return s.finish(projectID, sv, &MockResponse{
			StatusCode: StatusInternalServerError, ContentType: ContentTypeJSON,
			Body: errorBody("failed to build response example: " + err.Error()),
		})
	}

	return s.finish(projectID, sv, &MockResponse{
		StatusCode: StatusOK, ContentType: ContentTypeJSON, Body: body,
		Source: SourceSchemaExample, Matched: true,
	})
}
