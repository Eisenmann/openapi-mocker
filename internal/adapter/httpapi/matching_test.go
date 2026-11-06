package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestRouter_RequestMatchingEndToEnd(t *testing.T) {
	t.Parallel()

	h, id := newTemplatingRouter(t, "yaml", stateOpenAPI)
	rules := "/api/projects/" + id + "/mocks"

	create := func(body string) int {
		return call(t, h, http.MethodPost, rules, body).Code
	}

	if c := create(`{"path":"/users/{id}","method":"PUT","scenario":"default","statusCode":200,"body":"{\"r\":\"default\"}"}`); c != http.StatusCreated {
		t.Fatalf("default rule: %d", c)
	}

	vip := `{"path":"/users/{id}","method":"PUT","scenario":"vip","statusCode":202,"body":"{\"r\":\"vip\"}",` +
		`"match":[{"source":"body","name":"plan.tier","value":"vip"},{"source":"header","name":"X-Tenant","op":"exists"}]}`
	if c := create(vip); c != http.StatusCreated {
		t.Fatalf("conditional rule: %d", c)
	}

	badRegex := `{"path":"/users/{id}","method":"PUT","statusCode":200,"body":"{}","match":[{"source":"body","op":"regex","value":"("}]}`
	if c := create(badRegex); c != http.StatusBadRequest {
		t.Errorf("invalid regex must be 400, got %d", c)
	}

	badSource := `{"path":"/users/{id}","method":"PUT","statusCode":200,"body":"{}","match":[{"source":"cookie","name":"a","value":"b"}]}`
	if c := create(badSource); c != http.StatusBadRequest {
		t.Errorf("invalid source must be 400, got %d", c)
	}

	url := "/mock/" + id + "/users/1"

	rec := doRequest(t, h, http.MethodPut, url, strings.NewReader(`{"plan":{"tier":"vip"}}`), map[string]string{"X-Tenant": "acme"})
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "vip") {
		t.Errorf("both conditions hold: %d %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodPut, url, strings.NewReader(`{"plan":{"tier":"vip"}}`), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "default") {
		t.Errorf("missing header: default rule expected, got %d %s", rec.Code, rec.Body.String())
	}
}

const matchingMCP = `{"mcpServer":{"name":"demo","tools":[{"name":"weather",
 "mockResponses":{
  "default":{"content":[{"type":"text","text":"default"}]},
  "oslo":{"match":{"city":"Oslo"},"content":[{"type":"text","text":"oslo"}]}}}]}}`

func TestRouter_MCPRequestMatchingEndToEnd(t *testing.T) {
	t.Parallel()

	h, id := newTemplatingRouter(t, "mcp", matchingMCP)

	call := func(args string) string {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"weather","arguments":` + args + `}}`

		rec := doRequest(t, h, http.MethodPost, "/mock/"+id+"/mcp", strings.NewReader(body), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}

		return rec.Body.String()
	}

	if got := call(`{"city":"Oslo"}`); !strings.Contains(got, "oslo") || strings.Contains(got, `"match"`) {
		t.Errorf("matching arguments, no scenario header: %s", got)
	}

	if got := call(`{"city":"Rome"}`); !strings.Contains(got, "default") {
		t.Errorf("non-matching arguments: %s", got)
	}
}
