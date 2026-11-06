package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Record/replay proxy.
const (
	// HeaderForwarded marks a request the mock server sent upstream. A request
	// that arrives with it has come back to the mock server (the upstream URL
	// points at the mocker itself), so it is refused instead of looping.
	HeaderForwarded = "X-Openapi-Mocker-Forwarded"

	// Response sources of rule and example answers.
	SourceRule          = "rule"
	SourceSchemaExample = "schema-example"

	// Response sources of proxied requests.
	SourceProxyForward = "proxy-forward"
	SourceProxyRecord  = "proxy-record"
	SourceProxyError   = "proxy-error"

	// ScenarioRecorded names the rules saved from upstream responses.
	ScenarioRecorded = "recorded"

	StatusLoopDetected = 508
	StatusBadGateway   = http.StatusBadGateway

	// Recording limits: bigger exchanges are forwarded but not saved.
	maxRecordedBody    = 1 << 20
	maxRecordedReqBody = 64 << 10
)

// notRecordedHeaders are upstream response headers that are not saved in a
// recorded rule (and not passed on): they describe the upstream connection or
// session, or are set by the mock server itself.
var notRecordedHeaders = map[string]bool{
	"Set-Cookie": true, "Set-Cookie2": true, "Date": true, "Server": true, "Connection": true,
	"Transfer-Encoding": true, "Content-Length": true, "Content-Encoding": true, "Content-Type": true,
	"Keep-Alive": true, "Upgrade": true, "Trailer": true, "Te": true, "Vary": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Strict-Transport-Security": true,
	"Alt-Svc": true, "Via": true, "Age": true, "Etag": true, "Last-Modified": true,
}

// notForwardedHeaders are request headers that are not sent upstream.
var notForwardedHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Accept-Encoding": true, "Connection": true, "Keep-Alive": true,
	"Proxy-Connection": true, "Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

// forwardRequest sends req to the project's upstream. The path is appended to
// the upstream URL's own path; the query and the body are passed on.
func (s *MockServingService) forwardRequest(
	ctx context.Context, cfg domain.ProxyConfig, req *MockRequest,
) (*UpstreamResponse, error) {
	target, err := upstreamURL(cfg.UpstreamURL, req.Path, req.Query)
	if err != nil {
		return nil, err
	}

	header := make(map[string][]string, len(req.Header)+1)

	for name, values := range req.Header {
		canon := http.CanonicalHeaderKey(name)
		if notForwardedHeaders[canon] || strings.HasPrefix(canon, "X-Mock-") {
			continue
		}

		header[canon] = append([]string(nil), values...)
	}

	header[HeaderForwarded] = []string{"1"}

	return s.cfg.upstream.Do(ctx, &UpstreamRequest{
		Method: strings.ToUpper(req.Method), URL: target, Header: header, Body: req.Body,
	})
}

// upstreamURL joins the upstream base URL, the request path and the query.
func upstreamURL(base, path string, query map[string][]string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid upstream URL: %w", err)
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	u.Fragment = ""
	u.RawQuery = url.Values(query).Encode()

	return u.String(), nil
}

// proxy forwards a request no rule answers. ok is false when the project does
// not forward requests, so the caller carries on with its normal fallbacks.
func (s *MockServingService) proxy(
	projectID, pathTemplate string, req *MockRequest, tc *TemplateContext,
) (resp *MockResponse, ok bool) {
	cfg := s.cfg.proxyConfig(projectID)
	if !cfg.Mode.Forwards() {
		return nil, false
	}

	if _, looped := req.Header[HeaderForwarded]; looped {
		return &MockResponse{
			StatusCode: StatusLoopDetected, ContentType: ContentTypeJSON, Source: SourceProxyError,
			Body: errorBody("proxy loop detected: the upstream URL leads back to this mock server"),
		}, true
	}

	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}

	up, err := s.forwardRequest(ctx, cfg, req)
	if err != nil {
		return &MockResponse{
			StatusCode: StatusBadGateway, ContentType: ContentTypeJSON, Source: SourceProxyError,
			Body: errorBody("upstream request failed: " + err.Error()),
		}, true
	}

	resp = responseFromUpstream(up)
	resp.Source = SourceProxyForward

	if cfg.Mode == domain.ProxyRecord {
		resp.Source = SourceProxyRecord
		resp.MatchedRule = s.record(projectID, pathTemplate, tc, up)
	}

	return resp, true
}

// responseFromUpstream turns an upstream answer into a mock response.
func responseFromUpstream(up *UpstreamResponse) *MockResponse {
	return &MockResponse{
		StatusCode:  up.StatusCode,
		ContentType: contentTypeOf(up.Header),
		Headers:     recordedHeaders(up.Header),
		Body:        up.Body,
		Matched:     true,
	}
}

// recordedHeaders is the upstream response headers worth keeping, with
// repeated headers joined.
func recordedHeaders(header map[string][]string) map[string]string {
	out := map[string]string{}

	for name, values := range header {
		canon := http.CanonicalHeaderKey(name)
		if notRecordedHeaders[canon] || len(values) == 0 {
			continue
		}

		out[canon] = strings.Join(values, ", ")
	}

	return out
}

// contentTypeOf is the Content-Type of an upstream response.
func contentTypeOf(header map[string][]string) string {
	for k, v := range header {
		if strings.EqualFold(k, "Content-Type") && len(v) > 0 {
			return v[0]
		}
	}

	return ""
}

// record saves an upstream answer as a mock rule that serves exactly this
// request (same method, path, query and body) from then on. It returns the
// rule's ID, or "" when the exchange is not recordable: server errors are
// transient, and bodies that are binary or large do not belong in a mock.
func (s *MockServingService) record(
	projectID, pathTemplate string, tc *TemplateContext, up *UpstreamResponse,
) string {
	if up.StatusCode >= http.StatusInternalServerError ||
		len(up.Body) > maxRecordedBody || !utf8.Valid(up.Body) || len(tc.Body) > maxRecordedReqBody {
		return ""
	}

	key := requestKey(tc)

	s.recordMu.Lock()
	defer s.recordMu.Unlock()

	for _, m := range s.mocks.ListMocks(projectID) {
		if m.Recording != nil && m.Recording.Key == key && m.Path == pathTemplate &&
			strings.EqualFold(m.Method, tc.Method) {
			return m.ID
		}
	}

	rule := s.mocks.CreateMock(&domain.MockRule{
		ProjectID: projectID, Path: pathTemplate, Method: tc.Method, Scenario: ScenarioRecorded,
		StatusCode: up.StatusCode, ContentType: contentTypeOf(up.Header),
		Headers: recordedHeaders(up.Header), Body: string(up.Body), Raw: true,
		Recording: &domain.Recording{Key: key, Request: describeRequest(tc), RecordedAt: time.Now().UTC()},
	})

	return rule.ID
}

// describeRequest is "GET /users/7?limit=2".
func describeRequest(tc *TemplateContext) string {
	out := tc.Method + " " + tc.Path
	if q := url.Values(tc.Query).Encode(); q != "" {
		out += "?" + q
	}

	return out
}

// requestKey fingerprints a request: method, concrete path, query (order of
// parameters does not matter) and body (JSON is compared structurally).
func requestKey(tc *TemplateContext) string {
	names := make([]string, 0, len(tc.Query))
	for name := range tc.Query {
		names = append(names, name)
	}

	sort.Strings(names)

	var b strings.Builder

	b.WriteString(strings.ToUpper(tc.Method) + "\n" + tc.Path + "\n")

	for _, name := range names {
		for _, v := range tc.Query[name] {
			b.WriteString(url.QueryEscape(name) + "=" + url.QueryEscape(v) + "&")
		}
	}

	b.WriteString("\n" + normalizeBody(tc.Body))
	sum := sha256.Sum256([]byte(b.String()))

	return hex.EncodeToString(sum[:])
}

// normalizeBody makes equal JSON documents compare equal whatever their
// whitespace and key order; other bodies are used as they are.
func normalizeBody(body []byte) string {
	var v any

	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()

	err := dec.Decode(&v)
	if err != nil {
		return string(body)
	}

	out, err := json.Marshal(v)
	if err != nil {
		return string(body)
	}

	return string(out)
}

// recordedRule returns the rule recorded for exactly this request.
func recordedRule(rules []*domain.MockRule, tc *TemplateContext) *domain.MockRule {
	var key string

	for _, m := range rules {
		if m.Recording == nil {
			continue
		}

		if key == "" {
			key = requestKey(tc)
		}

		if m.Recording.Key == key {
			return m
		}
	}

	return nil
}
