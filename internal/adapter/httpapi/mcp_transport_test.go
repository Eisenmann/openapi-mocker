package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	transportMCP = `{"mcpServer":{"name":"t","tools":[{"name":"echo","mockResponses":{"default":{"content":[{"type":"text","text":"hi"}]}}}],
		"resources":[{"uri":"a://b","text":"x"}],
		"prompts":[{"name":"p","messages":[{"role":"user","content":"hi"}]}]}}`
	initBody  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	callBody  = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","_meta":{"progressToken":"tok"}}}`
	mcpURL    = "/mock/pv/mcp"
	jsonType  = "application/json"
	acceptSSE = "text/event-stream"
)

func post(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, h, http.MethodPost, mcpURL, strings.NewReader(body), headers)
}

func newSessionRouter(t *testing.T, require bool) http.Handler {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	logs := newMemLogRepo()
	p := projects.Create("v", "")
	contracts.AddVersion(p.ID, usecase.FormatMCP, transportMCP, "manual")

	return httpapi.NewRouter(&httpapi.Services{
		Projects:          usecase.NewProjectService(projects),
		MCPServing:        usecase.NewMCPServingService(contracts, logs, mcp.NewEngine(), usecase.WithProjects(projects)),
		MCPRequireSession: require,
	})
}

func TestMCPTransport_InitializeStartsSession(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)

	rec := post(t, h, initBody, nil)
	sid := rec.Header().Get("Mcp-Session-Id")

	if rec.Code != http.StatusOK || sid == "" {
		t.Fatalf("initialize: %d session=%q", rec.Code, sid)
	}

	// The session is accepted; an unknown one is a 404.
	if rec := post(t, h, callBody, map[string]string{"Mcp-Session-Id": sid}); rec.Code != http.StatusOK {
		t.Errorf("known session: %d", rec.Code)
	}

	if rec := post(t, h, callBody, map[string]string{"Mcp-Session-Id": "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown session: %d", rec.Code)
	}

	// Sessions are optional by default.
	if rec := post(t, h, callBody, nil); rec.Code != http.StatusOK {
		t.Errorf("no session: %d", rec.Code)
	}

	// A failed initialize does not start a session.
	if rec := post(t, h, `{"jsonrpc":"1.0","id":1,"method":"initialize"}`, nil); rec.Header().Get("Mcp-Session-Id") != "" {
		t.Error("an invalid initialize must not start a session")
	}
}

func TestMCPTransport_RequireSession(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, true)

	if rec := post(t, h, callBody, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing session: %d", rec.Code)
	}

	rec := post(t, h, initBody, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Mcp-Session-Id") == "" {
		t.Fatalf("initialize needs no session: %d", rec.Code)
	}

	headers := map[string]string{"Mcp-Session-Id": rec.Header().Get("Mcp-Session-Id")}
	if rec := post(t, h, callBody, headers); rec.Code != http.StatusOK {
		t.Errorf("with session: %d", rec.Code)
	}
}

func TestMCPTransport_ProtocolVersionHeader(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)

	if rec := post(t, h, callBody, map[string]string{"MCP-Protocol-Version": "1999-01-01"}); rec.Code != http.StatusBadRequest {
		t.Errorf("unsupported version: %d", rec.Code)
	}

	if rec := post(t, h, callBody, map[string]string{"MCP-Protocol-Version": "2025-06-18"}); rec.Code != http.StatusOK {
		t.Errorf("supported version: %d", rec.Code)
	}
}

func TestMCPTransport_DeleteEndsSession(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)
	sid := post(t, h, initBody, nil).Header().Get("Mcp-Session-Id")

	if rec := doRequest(t, h, http.MethodDelete, mcpURL, nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("delete without header: %d", rec.Code)
	}

	headers := map[string]string{"Mcp-Session-Id": sid}

	if rec := doRequest(t, h, http.MethodDelete, mcpURL, nil, headers); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}

	if rec := doRequest(t, h, http.MethodDelete, mcpURL, nil, headers); rec.Code != http.StatusNotFound {
		t.Errorf("delete twice: %d", rec.Code)
	}

	if rec := post(t, h, callBody, headers); rec.Code != http.StatusNotFound {
		t.Errorf("request on an ended session: %d", rec.Code)
	}
}

func TestMCPTransport_SSEResponses(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)

	// A client that accepts JSON and SSE gets JSON...
	rec := post(t, h, callBody, map[string]string{"Accept": jsonType + ", " + acceptSSE})
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), jsonType) {
		t.Errorf("both accepted: %q", rec.Header().Get("Content-Type"))
	}

	// ...unless it asks for a stream, or accepts nothing else.
	for _, headers := range []map[string]string{
		{"Accept": acceptSSE},
		{"Accept": jsonType + ", " + acceptSSE, "X-Mock-Stream": "sse"},
	} {
		rec = post(t, h, callBody, headers)
		if rec.Header().Get("Content-Type") != acceptSSE {
			t.Fatalf("%v: content type %q", headers, rec.Header().Get("Content-Type"))
		}

		body := rec.Body.String()
		if strings.Count(body, "event: message") != 3 || !strings.Contains(body, "notifications/progress") ||
			!strings.Contains(body, `"progressToken":"tok"`) || !strings.Contains(body, `"id":2`) {
			t.Errorf("want 2 progress events and the result:\n%s", body)
		}
	}

	// A notification stays a bodiless 202.
	rec = post(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{"Accept": acceptSSE})
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("notification: %d %q", rec.Code, rec.Body.String())
	}

	// A batch answer is split into one event per response.
	batch := `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"resources/list"}]`

	rec = post(t, h, batch, map[string]string{"Accept": acceptSSE})
	if strings.Count(rec.Body.String(), "event: message") != 2 {
		t.Errorf("batch events:\n%s", rec.Body.String())
	}
}

func TestMCPTransport_ResourcesAndPromptsOverHTTP(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)

	for method, want := range map[string]string{
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"a://b"}}`: `"text":"x"`,
		`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"p"}}`:       `"role":"user"`,
	} {
		if body := post(t, h, method, nil).Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s: %s", method, body)
		}
	}
}

func TestMCPTransport_GetStreamErrors(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)

	if rec := doRequest(t, h, http.MethodGet, mcpURL, nil, nil); rec.Code != http.StatusNotAcceptable {
		t.Errorf("GET without Accept: %d", rec.Code)
	}

	headers := map[string]string{"Accept": acceptSSE, "Mcp-Session-Id": "nope"}
	if rec := doRequest(t, h, http.MethodGet, mcpURL, nil, headers); rec.Code != http.StatusNotFound {
		t.Errorf("GET unknown session: %d", rec.Code)
	}

	if rec := doRequest(t, h, http.MethodGet, "/mock/missing/mcp", nil, map[string]string{"Accept": acceptSSE}); rec.Code < 400 {
		t.Errorf("GET unknown project: %d", rec.Code)
	}

	if rec := doRequest(t, newSessionRouter(t, true), http.MethodGet, mcpURL, nil,
		map[string]string{"Accept": acceptSSE}); rec.Code != http.StatusBadRequest {
		t.Errorf("GET without required session: %d", rec.Code)
	}
}

func TestMCPTransport_GetStreamKeepAliveAndSessionEnd(t *testing.T) {
	restore := httpapi.SetMCPKeepAlive(10 * time.Millisecond)
	defer restore()

	h := newSessionRouter(t, false)
	srv := httptest.NewServer(h)

	defer srv.Close()

	resp, err := http.Post(srv.URL+mcpURL, jsonType, strings.NewReader(initBody)) //nolint:noctx // test server.
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()
	sid := resp.Header.Get("Mcp-Session-Id")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+mcpURL, nil)
	req.Header.Set("Accept", acceptSSE)
	req.Header.Set("Mcp-Session-Id", sid)

	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer stream.Body.Close()

	if stream.Header.Get("Content-Type") != acceptSSE {
		t.Fatalf("content type %q", stream.Header.Get("Content-Type"))
	}

	sc := bufio.NewScanner(stream.Body)

	var lines []string

	for len(lines) < 3 && sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}

	if len(lines) < 2 || lines[0] != ": stream open" || lines[1] != ": keepalive" {
		t.Fatalf("stream lines: %v", lines)
	}

	// Ending the session closes the stream.
	delReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, srv.URL+mcpURL, nil)
	delReq.Header.Set("Mcp-Session-Id", sid)

	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}

	_ = delResp.Body.Close()

	for sc.Scan() { //nolint:revive // drain until the server closes the stream.
	}

	if ctx.Err() != nil {
		t.Error("the stream must end when its session is deleted")
	}
}

func TestMCPTransport_GetStreamEndsWithClient(t *testing.T) {
	restore := httpapi.SetMCPKeepAlive(time.Hour)
	defer restore()

	srv := httptest.NewServer(newSessionRouter(t, false))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+mcpURL, nil)
	req.Header.Set("Accept", acceptSSE)

	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = bufio.NewReader(stream.Body)

	cancel()
	_ = stream.Body.Close()
}

func TestMCPCatalogEndpoint(t *testing.T) {
	t.Parallel()

	h := newSessionRouter(t, false)

	rec := doRequest(t, h, http.MethodGet, "/api/projects/pv/mcp/catalog", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body.String())
	}

	for _, want := range []string{`"tools"`, `"resources"`, `"a://b"`, `"prompts"`, `"resourceTemplates"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("catalog lacks %s: %s", want, rec.Body.String())
		}
	}

	if rec := doRequest(t, h, http.MethodGet, "/api/projects/nope/mcp/catalog", nil, nil); rec.Code < 400 {
		t.Errorf("unknown project: %d", rec.Code)
	}

	disabled := httpapi.NewRouter(&httpapi.Services{})
	if rec := doRequest(t, disabled, http.MethodGet, "/api/projects/pv/mcp/catalog", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("disabled: %d", rec.Code)
	}
}

func TestMCPTransport_CORSExposesSessionHeader(t *testing.T) {
	t.Parallel()

	rec := doRequest(t, newSessionRouter(t, false), http.MethodOptions, mcpURL, nil, nil)

	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "Mcp-Session-Id") ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Mcp-Session-Id") {
		t.Errorf("CORS headers: %v", rec.Header())
	}
}
