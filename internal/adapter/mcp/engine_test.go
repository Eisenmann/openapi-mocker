package mcp

import (
	"encoding/json"
	"testing"
)

const canonicalManifest = `{
	"mcpServer": {
		"name": "weather-mcp",
		"version": "1.2.0",
		"tools": [
			{
				"name": "get_forecast",
				"description": "Get the weather forecast for a city",
				"inputSchema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]},
				"mockResponses": {
					"default": {"forecast": "sunny", "high": 25},
					"rainy": {"forecast": "rain", "high": 18}
				}
			},
			{
				"name": "echo",
				"description": "Echo back the input",
				"mockResponses": {
					"default": {"type": "text", "text": "pong"}
				}
			}
		]
	}
}`

const flatManifest = `{
	"serverInfo": {"name": "flat-server", "version": "0.1.0"},
	"tools": [
		{"name": "only_tool", "description": "the only tool"}
	]
}`

func TestParseCanonicalLayout(t *testing.T) {
	m, err := parse([]byte(canonicalManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "weather-mcp" || m.Version != "1.2.0" {
		t.Fatalf("got %q %q", m.Name, m.Version)
	}
	if len(m.Tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(m.Tools))
	}
}

func TestParseFlatLayout(t *testing.T) {
	m, err := parse([]byte(flatManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "flat-server" {
		t.Fatalf("got %q", m.Name)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(m.Tools))
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"not json", "nope"},
		{"no tools", `{"mcpServer": {"name": "x"}}`},
		{"empty tool name", `{"tools": [{"name": ""}]}`},
		{"duplicate tool", `{"tools": [{"name": "a"},{"name": "a"}]}`},
	}
	for _, tc := range cases {
		if _, err := parse([]byte(tc.raw)); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestExecuteInitialize(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "id": 1, "method": "initialize"}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.ServerInfo.Name != "weather-mcp" {
		t.Fatalf("got %q", resp.Result.ServerInfo.Name)
	}
}

func TestExecuteToolsList(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Result.Tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(resp.Result.Tools))
	}
	if resp.Result.Tools[0]["name"] != "get_forecast" {
		t.Fatalf("got %v", resp.Result.Tools[0]["name"])
	}
}

func TestExecuteToolsCallDefault(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "get_forecast", "arguments": {"city": "Berlin"}}}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertWrapped(t, resp.Result, "sunny")
}

func TestExecuteToolsCallScenario(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "get_forecast"}}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "rainy")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertWrapped(t, resp.Result, "rain")
}

func TestExecuteNotificationNoBody(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "method": "notifications/initialized"}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != nil {
		t.Fatalf("notification must produce no body, got %s", out)
	}
}

func TestExecuteUnknownMethod(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "id": 5, "method": "resources/list"}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Code != codeMethodNotFound {
		t.Fatalf("want %d, got %d", codeMethodNotFound, resp.Error.Code)
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	e := NewEngine()
	req := `{"jsonrpc": "2.0", "id": 6, "method": "tools/call", "params": {"name": "nope"}}`
	out, err := e.Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Code != codeInvalidParams {
		t.Fatalf("want %d, got %d", codeInvalidParams, resp.Error.Code)
	}
}

func TestExecuteInvalidRequestJSON(t *testing.T) {
	e := NewEngine()
	out, err := e.Execute([]byte(canonicalManifest), []byte("{bad json"), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Code != codeParseError {
		t.Fatalf("want %d, got %d", codeParseError, resp.Error.Code)
	}
}

// assertWrapped checks that a plain-object mock became a valid CallToolResult.
func assertWrapped(t *testing.T, result map[string]any, forecast string) {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("result must carry one content item, got %v", result["content"])
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok || sc["forecast"] != forecast {
		t.Fatalf("structuredContent mismatch: %v", result["structuredContent"])
	}
}

func TestExecuteToolsCallPassthroughContent(t *testing.T) {
	const manifest = `{"mcpServer":{"tools":[{"name":"t","mockResponses":{"default":{"content":[{"type":"text","text":"hi"}],"isError":true}}}]}}`
	out, err := NewEngine().Execute([]byte(manifest), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t"}}`), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result["isError"] != true {
		t.Fatalf("CallToolResult must pass through verbatim, got %v", resp.Result)
	}
	if _, has := resp.Result["structuredContent"]; has {
		t.Fatalf("verbatim result must not be rewrapped")
	}
}

func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	cases := map[string]string{
		`{"protocolVersion":"2025-03-26"}`: "2025-03-26",
		`{"protocolVersion":"1999-01-01"}`: "2025-06-18",
		`{}`:                               "2025-06-18",
	}
	for params, want := range cases {
		req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` + params + `}`
		out, err := NewEngine().Execute([]byte(canonicalManifest), []byte(req), "")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var resp struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Result.ProtocolVersion != want {
			t.Fatalf("params %s: want %s, got %s", params, want, resp.Result.ProtocolVersion)
		}
	}
}

func TestExecuteBatch(t *testing.T) {
	req := `[
		{"jsonrpc":"2.0","id":1,"method":"ping"},
		{"jsonrpc":"2.0","method":"notifications/initialized"},
		{"jsonrpc":"2.0","id":"b","method":"tools/list"}
	]`
	out, err := NewEngine().Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp []map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("batch response must be an array: %v (%s)", err, out)
	}
	if len(resp) != 2 {
		t.Fatalf("notification must be skipped; want 2 responses, got %d", len(resp))
	}
}

func TestExecuteBatchOnlyNotifications(t *testing.T) {
	out, err := NewEngine().Execute([]byte(canonicalManifest), []byte(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`), "")
	if err != nil || out != nil {
		t.Fatalf("want (nil, nil), got (%s, %v)", out, err)
	}
}

func TestExecuteEmptyBatch(t *testing.T) {
	assertErrorCode(t, `[]`, codeInvalidRequest)
}

func TestExecuteInvalidRequestShape(t *testing.T) {
	assertErrorCode(t, `{"id":1,"method":"ping"}`, codeInvalidRequest)
	assertErrorCode(t, `{"jsonrpc":"2.0","id":1}`, codeInvalidRequest)
}

func assertErrorCode(t *testing.T, req string, want int) {
	t.Helper()
	out, err := NewEngine().Execute([]byte(canonicalManifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if resp.Error.Code != want {
		t.Fatalf("%s: want %d, got %d", req, want, resp.Error.Code)
	}
}

func TestMockResponseLookup(t *testing.T) {
	const manifest = `{"mcpServer":{"tools":[
		{"name":"lone","mockResponses":{"only":{"content":[{"type":"text","text":"x"}]}}},
		{"name":"two","mockResponses":{"default":{"content":[{"type":"text","text":"d"}]},"a":{"content":[{"type":"text","text":"a"}]}}}
	]}}`
	call := func(tool, scenario string) map[string]any {
		out, err := NewEngine().Execute([]byte(manifest), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`"}}`), scenario)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var resp map[string]any
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}
	text := func(resp map[string]any) string {
		res, _ := resp["result"].(map[string]any)
		c, _ := res["content"].([]any)
		if len(c) == 0 {
			return ""
		}
		return c[0].(map[string]any)["text"].(string)
	}

	if got := text(call("lone", "")); got != "x" {
		t.Fatalf("lone response without scenario: got %q", got)
	}
	if resp := call("lone", "typo"); resp["error"] == nil {
		t.Fatalf("unknown scenario must not fall back to an unrelated lone response")
	}
	if got := text(call("two", "a")); got != "a" {
		t.Fatalf("scenario a: got %q", got)
	}
	if got := text(call("two", "typo")); got != "d" {
		t.Fatalf("unknown scenario must fall back to default, got %q", got)
	}
}
