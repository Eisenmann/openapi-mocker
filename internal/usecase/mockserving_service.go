package usecase

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
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

	contract, err := s.contracts.GetActive(projectID)
	if err != nil {
		return s.finish(projectID, method, path, &MockResponse{
			StatusCode: StatusNotFound, ContentType: ContentTypeJSON,
			Body: errorBody("no contract has been uploaded for this project yet"),
		})
	}

	pathTemplate, _, found := s.engine.FindOperation([]byte(contract.Raw), method, path)
	if !found {
		return s.finish(projectID, method, path, &MockResponse{
			StatusCode: StatusNotFound, ContentType: ContentTypeJSON,
			Body: errorBody(fmt.Sprintf("endpoint not described in contract: %s %s", method, path)),
		})
	}

	violations := s.validate(projectID, contract.Raw, req)
	if len(violations) > 0 && s.cfg.validationMode(projectID) == domain.ValidationEnforce {
		return s.finish(projectID, method, path, rejectInvalidRequest(violations))
	}

	resp := s.respond(projectID, contract.Raw, pathTemplate, req)
	if len(violations) > 0 {
		resp = withWarnings(resp, violations)
	}

	return s.finish(projectID, method, path, resp)
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

	if rule := s.findRule(projectID, pathTemplate, method, scenario); rule != nil {
		return s.fromRule(rule)
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

	return &MockResponse{
		StatusCode: code, ContentType: nonEmpty(contentType, ContentTypeJSON),
		Body: body, Source: "schema-example", Matched: true,
	}
}

func (s *MockServingService) findRule(projectID, pathTemplate, method, scenario string) *domain.MockRule {
	method = strings.ToUpper(method)

	var fallback *domain.MockRule

	for _, m := range s.mocks.ListMocks(projectID) {
		if !strings.EqualFold(m.Method, method) || m.Path != pathTemplate {
			continue
		}

		if scenario != "" && m.Scenario == scenario {
			return m
		}

		if m.Scenario == "" || m.Scenario == "default" {
			fallback = m
		}
	}

	return fallback
}

func (s *MockServingService) fromRule(rule *domain.MockRule) *MockResponse {
	// Chaos testing: with probability FailRatePct, return a random 5xx error,
	// even when the rule describes a successful scenario.
	if rule.FailRatePct > 0 && rand.Intn(PercentBase) < rule.FailRatePct {
		code := StatusInternalServerError + rand.Intn(ServerErrorRange) // 500-503

		return &MockResponse{
			StatusCode: code, ContentType: ContentTypeJSON,
			Body:    errorBody("injected failure for chaos testing"),
			DelayMs: rule.DelayMs, Source: "chaos-injection", MatchedRule: rule.ID, Matched: true,
		}
	}

	code := rule.StatusCode
	if code == 0 {
		code = 200
	}

	return &MockResponse{
		StatusCode: code, ContentType: nonEmpty(rule.ContentType, ContentTypeJSON),
		Headers: rule.Headers, Body: []byte(rule.Body), DelayMs: rule.DelayMs,
		Source: "rule", MatchedRule: rule.ID, Matched: true,
	}
}

func (s *MockServingService) finish(projectID, method, path string, resp *MockResponse) MockResponse {
	s.logs.Add(&domain.RequestLog{
		ProjectID: projectID, Method: method, Path: path,
		StatusCode: resp.StatusCode, MatchedRule: resp.MatchedRule, Matched: resp.Matched,
		Violations: resp.Violations, Timestamp: time.Now().UTC(),
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
