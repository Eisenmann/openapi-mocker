package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const validationOpenAPI = `openapi: 3.0.0
info: {title: Orders, version: 1.0.0}
paths:
  /orders:
    get:
      parameters:
        - {name: limit, in: query, schema: {type: integer, maximum: 100}}
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema: {type: array, items: {type: object}}
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties: {name: {type: string}}
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema: {type: object}
`

const validationMCP = `{"mcpServer":{"name":"demo","tools":[{"name":"echo",
	"inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},
	"mockResponses":{"default":{"content":[{"type":"text","text":"hi"}]}}}]}}`

// newValidationRouter wires the real OpenAPI and MCP engines to in-memory
// repositories, with one project ("pv") holding the given contract.
func newValidationRouter(t *testing.T, format, raw string) (http.Handler, *memProjectRepo, *memLogRepo) {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	logs := newMemLogRepo()
	engine := openapi.NewEngine()

	p := projects.Create("v", "")
	contracts.AddVersion(p.ID, format, raw, "manual")

	svc := httpapi.Services{
		Projects: usecase.NewProjectService(projects),
		MockServing: usecase.NewMockServingService(
			contracts, newMemMockRepo(), logs, engine, usecase.WithProjects(projects),
		),
		MCPServing: usecase.NewMCPServingService(contracts, logs, mcp.NewEngine(), usecase.WithProjects(projects)),
		Logs:       usecase.NewLogService(logs),
	}

	return httpapi.NewRouter(&svc), projects, logs
}

func setMode(t *testing.T, h http.Handler, id, mode string) {
	t.Helper()

	rec := doRequest(t, h, http.MethodPatch, "/api/projects/"+id,
		strings.NewReader(`{"validationMode":"`+mode+`"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set mode %s: %d %s", mode, rec.Code, rec.Body.String())
	}
}

func TestRouter_PatchProjectValidationMode(t *testing.T) {
	t.Parallel()

	h, _, _ := newValidationRouter(t, "yaml", validationOpenAPI)

	rec := doRequest(t, h, http.MethodPatch, "/api/projects/pv", strings.NewReader(`{"validationMode":"warn"}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var p domain.Project
	decodeJSON(t, rec, &p)

	if p.ValidationMode != domain.ValidationWarn {
		t.Errorf("mode not updated: %+v", p)
	}

	// The mode is persisted: GET returns it.
	var got domain.Project
	decodeJSON(t, doRequest(t, h, http.MethodGet, "/api/projects/pv", nil, nil), &got)

	if got.ValidationMode != domain.ValidationWarn {
		t.Errorf("GET must return the new mode, got %q", got.ValidationMode)
	}

	cases := []struct {
		name, path, body string
		want             int
	}{
		{"invalid mode", "/api/projects/pv", `{"validationMode":"strict"}`, http.StatusBadRequest},
		{"invalid json", "/api/projects/pv", `{`, http.StatusBadRequest},
		{"unknown project", "/api/projects/nope", `{"validationMode":"warn"}`, http.StatusNotFound},
		{"nothing to change", "/api/projects/pv", `{}`, http.StatusOK},
	}

	for _, tc := range cases {
		rec := doRequest(t, h, http.MethodPatch, tc.path, strings.NewReader(tc.body), nil)
		if rec.Code != tc.want {
			t.Errorf("%s: want %d, got %d (%s)", tc.name, tc.want, rec.Code, rec.Body.String())
		}
	}
}

func postOrder(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, h, http.MethodPost, "/mock/pv/orders", strings.NewReader(body),
		map[string]string{"Content-Type": "application/json"})
}

func TestRouter_RequestValidationEndToEnd(t *testing.T) {
	t.Parallel()

	h, _, logs := newValidationRouter(t, "yaml", validationOpenAPI)

	// Default (off): a bad request is served as before.
	if rec := postOrder(t, h, `{"name": 1}`); rec.Code != http.StatusOK || rec.Header().Get("X-Mock-Validation") != "" {
		t.Errorf("off: want plain 200, got %d %v", rec.Code, rec.Header())
	}

	// Warn: served, flagged in a header and in the log.
	setMode(t, h, "pv", "warn")

	rec := postOrder(t, h, `{"name": 1}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("X-Mock-Validation"), "violation") {
		t.Errorf("warn: want 200 with warning header, got %d %v", rec.Code, rec.Header())
	}

	if l := logs.ListLogs("pv", 1)[0]; len(l.Violations) == 0 {
		t.Errorf("warn: violations must be logged, got %+v", l)
	}

	// Enforce: bad requests get 400 with details; good ones are served.
	setMode(t, h, "pv", "enforce")

	rec = postOrder(t, h, `{"name": 1}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request body.name") {
		t.Errorf("enforce: want 400 naming the field, got %d %s", rec.Code, rec.Body.String())
	}

	if rec.Header().Get("X-Mock-Source") != "request-validation" {
		t.Errorf("enforce: X-Mock-Source should say request-validation, got %v", rec.Header())
	}

	if rec = postOrder(t, h, `{"name": "ok"}`); rec.Code != http.StatusOK {
		t.Errorf("enforce: valid request must be served, got %d %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodGet, "/mock/pv/orders?limit=1000", nil, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
		t.Errorf("enforce: bad query param must be rejected, got %d %s", rec.Code, rec.Body.String())
	}

	if rec = doRequest(t, h, http.MethodGet, "/mock/pv/orders?limit=5", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("enforce: valid query must be served, got %d", rec.Code)
	}
}

func TestRouter_MCPValidationEndToEnd(t *testing.T) {
	t.Parallel()

	h, _, logs := newValidationRouter(t, usecase.FormatMCP, validationMCP)

	const badCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`

	call := func() *httptest.ResponseRecorder {
		return doRequest(t, h, http.MethodPost, "/mock/pv/mcp", strings.NewReader(badCall), nil)
	}

	// Off: served.
	if rec := call(); !strings.Contains(rec.Body.String(), `"result"`) {
		t.Errorf("off: want a result, got %s", rec.Body.String())
	}

	// Warn: served, with the violations visible in a header and in _meta.
	setMode(t, h, "pv", "warn")

	rec := call()
	if !strings.Contains(rec.Body.String(), `"result"`) || !strings.Contains(rec.Body.String(), "validationWarnings") {
		t.Errorf("warn: want a result carrying _meta warnings, got %s", rec.Body.String())
	}

	if got := rec.Header().Get("X-Mock-Validation"); !strings.HasPrefix(got, "warn: 1 violation") {
		t.Errorf("warn: want X-Mock-Validation header, got %q", got)
	}

	// Enforce: JSON-RPC invalid params with details, still HTTP 200.
	setMode(t, h, "pv", "enforce")

	rec = call()
	if got := rec.Header().Get("X-Mock-Validation"); !strings.HasPrefix(got, "rejected: 1 violation") {
		t.Errorf("enforce: want rejected header, got %q", got)
	}

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "-32602") ||
		!strings.Contains(rec.Body.String(), "text") {
		t.Errorf("enforce: want -32602 naming the property, got %d %s", rec.Code, rec.Body.String())
	}

	if l := logs.ListLogs("pv", 1)[0]; l.Matched || len(l.Violations) == 0 {
		t.Errorf("enforce: rejected call must be logged unmatched with violations: %+v", l)
	}

	// Notifications are still accepted without a body.
	rec = doRequest(t, h, http.MethodPost, "/mock/pv/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), nil)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("notification: want empty 202, got %d %q", rec.Code, rec.Body.String())
	}
}
