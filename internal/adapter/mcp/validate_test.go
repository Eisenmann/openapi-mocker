package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

const validationManifest = `{
	"mcpServer": {
		"name": "demo",
		"tools": [
			{
				"name": "get_forecast",
				"inputSchema": {
					"type": "object",
					"properties": {
						"city": {"type": "string"},
						"days": {"type": "integer", "minimum": 1, "maximum": 7}
					},
					"required": ["city"],
					"additionalProperties": false
				},
				"mockResponses": {"default": {"content": [{"type": "text", "text": "sunny"}]}}
			},
			{
				"name": "no_schema",
				"mockResponses": {"default": {"content": [{"type": "text", "text": "ok"}]}}
			}
		]
	}
}`

func callReq(tool, args string) []byte {
	params := `{"name":"` + tool + `"`
	if args != "" {
		params += `,"arguments":` + args
	}

	params += `}`

	return []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}`)
}

type rpcOut struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code    int      `json:"code"`
		Message string   `json:"message"`
		Data    []string `json:"data"`
	} `json:"error"`
}

func runValidated(t *testing.T, req []byte, enforce bool) (rpcOut, []string) {
	t.Helper()

	out, violations, err := NewEngine().ExecuteValidated([]byte(validationManifest), req, "", enforce)
	if err != nil {
		t.Fatalf("ExecuteValidated: %v", err)
	}

	var resp rpcOut
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}

	return resp, violations
}

func TestExecuteValidated_ValidCall(t *testing.T) {
	t.Parallel()

	for _, enforce := range []bool{false, true} {
		resp, violations := runValidated(t, callReq("get_forecast", `{"city":"Berlin","days":3}`), enforce)
		if len(violations) != 0 || resp.Error != nil || resp.Result == nil {
			t.Errorf("enforce=%v: valid call must be served, got violations=%v err=%v", enforce, violations, resp.Error)
		}
	}
}

func TestExecuteValidated_EnforceRejects(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args string
		want string
	}{
		"missing required": {`{}`, "city"},
		"no arguments":     {``, "city"},
		"null arguments":   {`null`, "city"},
		"wrong type":       {`{"city":5}`, "city"},
		"out of range":     {`{"city":"x","days":99}`, "days"},
		"extra property":   {`{"city":"x","unit":"c"}`, "unit"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resp, violations := runValidated(t, callReq("get_forecast", tc.args), true)
			if resp.Error == nil || resp.Error.Code != codeInvalidParams {
				t.Fatalf("want -32602 error, got %+v", resp)
			}

			if len(resp.Error.Data) == 0 || !strings.Contains(strings.Join(resp.Error.Data, "|"), tc.want) {
				t.Errorf("error data %v should mention %q", resp.Error.Data, tc.want)
			}

			if len(violations) == 0 || !strings.HasPrefix(violations[0], "tools/call get_forecast: ") {
				t.Errorf("violations should be returned for logging, got %v", violations)
			}
		})
	}
}

func TestExecuteValidated_WarnServesAndReports(t *testing.T) {
	t.Parallel()

	resp, violations := runValidated(t, callReq("get_forecast", `{}`), false)
	if resp.Error != nil || resp.Result == nil {
		t.Fatalf("warn mode must still serve the mock, got %+v", resp)
	}

	if len(violations) == 0 {
		t.Error("warn mode must report the violation")
	}
}

func TestExecuteValidated_WarnAttachesMeta(t *testing.T) {
	t.Parallel()

	resp, _ := runValidated(t, callReq("get_forecast", `{}`), false)

	meta, ok := resp.Result["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("warn mode must attach _meta to the result, got %v", resp.Result)
	}

	warnings, _ := meta[warningsMetaKey].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), "city") {
		t.Errorf("_meta must list the violations, got %v", meta)
	}

	// The mock's own content is untouched.
	if _, ok := resp.Result["content"]; !ok {
		t.Error("mock content must still be returned")
	}

	// A valid call and enforce mode carry no warnings.
	valid, _ := runValidated(t, callReq("get_forecast", `{"city":"x"}`), false)
	if _, has := valid.Result["_meta"]; has {
		t.Error("valid call must not carry _meta")
	}
}

func TestWithValidationWarnings_KeepsExistingMeta(t *testing.T) {
	t.Parallel()

	result := map[string]any{"content": []any{}, "_meta": map[string]any{"mine": 1}}
	got := withValidationWarnings(result, []string{"bad"})

	meta := got["_meta"].(map[string]any)
	if meta["mine"] != 1 || meta[warningsMetaKey] == nil {
		t.Errorf("existing _meta must be merged, got %v", meta)
	}

	if out := withValidationWarnings(map[string]any{"a": 1}, nil); out["_meta"] != nil {
		t.Error("no violations, no _meta")
	}
}

func TestExecuteValidated_NoSchemaAlwaysValid(t *testing.T) {
	t.Parallel()

	resp, violations := runValidated(t, callReq("no_schema", `{"anything":1}`), true)
	if resp.Error != nil || len(violations) != 0 {
		t.Errorf("tool without inputSchema must not be validated, got %+v %v", resp.Error, violations)
	}
}

func TestExecuteValidated_Batch(t *testing.T) {
	t.Parallel()

	req := []byte(`[
		{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_forecast","arguments":{"city":"A"}}},
		{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_forecast","arguments":{}}}
	]`)

	out, violations, err := NewEngine().ExecuteValidated([]byte(validationManifest), req, "", true)
	if err != nil {
		t.Fatalf("ExecuteValidated: %v", err)
	}

	var resp []rpcOut
	if err := json.Unmarshal(out, &resp); err != nil || len(resp) != 2 {
		t.Fatalf("want 2 responses, got %s (%v)", out, err)
	}

	if resp[0].Error != nil || resp[1].Error == nil {
		t.Errorf("only the invalid call of the batch must be rejected: %+v", resp)
	}

	if len(violations) != 1 {
		t.Errorf("want 1 violation, got %v", violations)
	}
}

func TestExecute_NeverValidates(t *testing.T) {
	t.Parallel()

	out, err := NewEngine().Execute([]byte(validationManifest), callReq("get_forecast", `{}`), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var resp rpcOut
	if err := json.Unmarshal(out, &resp); err != nil || resp.Error != nil {
		t.Errorf("plain Execute must not validate: %s", out)
	}
}
