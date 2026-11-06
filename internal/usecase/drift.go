package usecase

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	driftTimeout      = 20 * time.Second
	maxParallelDrifts = 4
	maxDifferences    = 50
	kindNull          = "null"
)

// WaitDrift blocks until the drift checks started so far are done. Tests and
// a graceful shutdown use it; requests never wait for a check.
func (s *MockServingService) WaitDrift() {
	s.driftWG.Wait()
}

// startDrift compares, in the background, what the mock just answered with
// what the upstream answers to the same request. Only safe methods are
// replayed (replaying a POST would repeat its side effects upstream), and
// only answers made up by the mock are checked, not proxied or injected ones.
func (s *MockServingService) startDrift(projectID, raw string, req *MockRequest, resp *MockResponse) {
	cfg := s.cfg.proxyConfig(projectID)
	if !cfg.DriftCheck || s.cfg.drifts == nil || !driftApplies(req, resp) {
		return
	}

	select {
	case s.driftSem <- struct{}{}:
	default:
		return // enough checks are running already; drift detection is best effort.
	}

	probe := &MockRequest{
		Method: req.Method, Path: req.Path, Query: req.Query, Header: req.Header, Body: nil,
		SkipValidation: true,
	}
	mockStatus, mockType, mockBody := resp.StatusCode, resp.ContentType, resp.Body
	label := SourceSchemaExample

	if resp.Source == SourceRule {
		label = "rule " + resp.MatchedRule
	}

	s.driftWG.Add(1)

	go func() {
		defer s.driftWG.Done()
		defer func() { <-s.driftSem }()

		s.checkDrift(projectID, raw, cfg, probe, &MockResponse{
			StatusCode: mockStatus, ContentType: mockType, Body: mockBody, Source: label,
		})
	}()
}

// driftApplies tells whether a served response is one to check: made up by
// the mock (not proxied, not an injected fault), for a request that is safe to
// replay and did not come from this mock server itself.
func driftApplies(req *MockRequest, resp *MockResponse) bool {
	if resp.Fault != "" || (resp.Source != SourceRule && resp.Source != SourceSchemaExample) {
		return false
	}

	if method := strings.ToUpper(req.Method); method != http.MethodGet && method != http.MethodHead {
		return false
	}

	_, looped := req.Header[HeaderForwarded]

	return !looped
}

// checkDrift replays the request upstream and stores the comparison.
func (s *MockServingService) checkDrift(
	projectID, raw string, cfg domain.ProxyConfig, req *MockRequest, mock *MockResponse,
) {
	ctx, cancel := context.WithTimeout(context.Background(), driftTimeout)
	defer cancel()

	started := time.Now()
	report := &domain.DriftReport{
		ProjectID: projectID, Method: strings.ToUpper(req.Method), Path: req.Path,
		Mock: mock.Source, MockStatus: mock.StatusCode, Timestamp: started.UTC(),
	}

	live, err := s.forwardRequest(ctx, cfg, req)
	if err != nil {
		report.Differences = []domain.DriftDifference{{Kind: domain.DriftUpstreamError, Live: err.Error()}}
	} else {
		report.LiveStatus = live.StatusCode
		report.Differences = compareResponses(mock, live)

		if more := s.contractDrift(raw, req, live); more != nil {
			report.Differences = append(report.Differences, *more)
		}
	}

	report.DurationMs = time.Since(started).Milliseconds()
	report.Drifted = len(report.Differences) > 0
	s.cfg.drifts.AddDrift(report)
}

// contractDrift reports a live answer that breaks the contract itself.
func (s *MockServingService) contractDrift(
	raw string, req *MockRequest, live *UpstreamResponse,
) *domain.DriftDifference {
	if !isJSONType(contentTypeOf(live.Header)) {
		return nil
	}

	err := s.engine.ValidateResponseBody([]byte(raw), req.Method, req.Path, strconv.Itoa(live.StatusCode), live.Body)
	if err == nil {
		return nil
	}

	return &domain.DriftDifference{Kind: domain.DriftContract, Live: err.Error(), Path: "", Mock: ""}
}

// compareResponses lists how the live answer differs from the mock's: status,
// media type and the structure of JSON bodies (fields and their types, not
// their values, which are expected to differ).
func compareResponses(mock *MockResponse, live *UpstreamResponse) []domain.DriftDifference {
	var diffs []domain.DriftDifference

	if mock.StatusCode != live.StatusCode {
		return append(diffs, domain.DriftDifference{
			Kind: domain.DriftStatus, Path: "",
			Mock: strconv.Itoa(mock.StatusCode), Live: strconv.Itoa(live.StatusCode),
		})
	}

	mockType, liveType := mediaType(mock.ContentType), mediaType(contentTypeOf(live.Header))
	if mockType != liveType {
		diffs = append(diffs, domain.DriftDifference{Kind: domain.DriftContentType, Path: "", Mock: mockType, Live: liveType})
	}

	if !isJSONType(mockType) || !isJSONType(liveType) {
		return diffs
	}

	mockJSON, mockOK := parseJSON(mock.Body)
	liveJSON, liveOK := parseJSON(live.Body)

	switch {
	case mockOK && !liveOK:
		diffs = append(diffs, domain.DriftDifference{
			Kind: domain.DriftBodyNotJSON, Path: "", Mock: "json", Live: "not json",
		})
	case mockOK && liveOK:
		diffs = diffJSON(diffs, "", mockJSON, liveJSON)
	}

	return diffs
}

func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(contentType))
	}

	return mt
}

func isJSONType(contentType string) bool {
	mt := mediaType(contentType)

	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// diffJSON appends the structural differences between two JSON values. For
// arrays only the first elements are compared: the items are expected to
// share one shape.
func diffJSON(diffs []domain.DriftDifference, path string, mock, live any) []domain.DriftDifference {
	if len(diffs) >= maxDifferences {
		return diffs
	}

	mk, lk := jsonKind(mock), jsonKind(live)
	if mk != lk {
		return append(diffs, domain.DriftDifference{Kind: domain.DriftType, Path: path, Mock: mk, Live: lk})
	}

	switch m := mock.(type) {
	case map[string]any:
		return diffObjects(diffs, path, m, live.(map[string]any)) //nolint:forcetypeassert // kinds are equal.
	case []any:
		l := live.([]any) //nolint:forcetypeassert // kinds are equal.
		if len(m) > 0 && len(l) > 0 {
			return diffJSON(diffs, path+"[0]", m[0], l[0])
		}
	}

	return diffs
}

func diffObjects(diffs []domain.DriftDifference, path string, mock, live map[string]any) []domain.DriftDifference {
	for _, k := range unionKeys(mock, live) {
		if len(diffs) >= maxDifferences {
			break
		}

		child := k
		if path != "" {
			child = path + "." + k
		}

		mv, inMock := mock[k]
		lv, inLive := live[k]

		switch {
		case inMock && !inLive:
			diffs = append(diffs, domain.DriftDifference{Kind: domain.DriftMissing, Path: child, Mock: jsonKind(mv), Live: ""})
		case !inMock && inLive:
			diffs = append(diffs, domain.DriftDifference{Kind: domain.DriftExtra, Path: child, Mock: "", Live: jsonKind(lv)})
		default:
			diffs = diffJSON(diffs, child, mv, lv)
		}
	}

	return diffs
}

// unionKeys is the sorted keys of both objects.
func unionKeys(a, b map[string]any) []string {
	seen := make(map[string]bool, len(a)+len(b))
	keys := make([]string, 0, len(a)+len(b))

	for _, m := range []map[string]any{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	sort.Strings(keys)

	return keys
}

// jsonKind names the JSON type of a decoded value; whole numbers and decimals
// are both "number".
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return kindNull
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "number"
	}
}

func parseJSON(body []byte) (any, bool) {
	var v any

	err := json.Unmarshal(body, &v)

	return v, err == nil
}
