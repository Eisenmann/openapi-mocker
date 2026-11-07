package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const richManifest = `{
	"mcpServer": {
		"name": "rich",
		"tools": [
			{
				"name": "lookup",
				"title": "Look up a user",
				"description": "d",
				"outputSchema": {"type": "object", "properties": {"id": {"type": "integer"}}},
				"annotations": {"readOnlyHint": true},
				"mockResponses": {"default": {"content": [{"type": "text", "text": "{\"id\": 7}"}]}}
			},
			{"name": "plain", "mockResponses": {"default": {"a": 1}}}
		],
		"resources": [
			{"uri": "file:///readme.md", "name": "readme", "title": "Readme", "description": "docs",
			 "mimeType": "text/markdown", "text": "# Hello", "annotations": {"priority": 1}},
			{"uri": "file:///config.json", "text": {"debug": true}},
			{"uri": "file:///logo.png", "mimeType": "image/png", "blob": "aGk="},
			{"uri": "file:///empty"}
		],
		"resourceTemplates": [
			{"uriTemplate": "users://{id}/profile", "name": "profile", "text": "user {{id}}"},
			{"uriTemplate": "docs://{+path}", "name": "docs"},
			{"uriTemplate": "search://items{?q,limit}", "name": "search", "text": "results"}
		],
		"prompts": [
			{
				"name": "review",
				"title": "Review code",
				"description": "Reviews code",
				"arguments": [{"name": "code", "required": true}, {"name": "lang"}],
				"messages": [
					{"role": "user", "content": "Review this {{lang}} code: {{code}}"},
					{"role": "assistant", "content": {"type": "resource", "resource": {"uri": "file:///{{lang}}", "text": "{{code}}"}}}
				]
			}
		]
	}
}`

// rpc runs one request against the manifest and decodes the response.
func rpc(t *testing.T, manifest, req string) map[string]any {
	t.Helper()

	out, err := NewEngine().Execute([]byte(manifest), []byte(req), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var resp map[string]any

	err = json.Unmarshal(out, &resp)
	if err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}

	return resp
}

func result(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()

	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("want a result, got %v", resp)
	}

	return res
}

func errorCode(t *testing.T, resp map[string]any) int {
	t.Helper()

	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("want an error, got %v", resp)
	}

	code, _ := e["code"].(float64)

	return int(code)
}

func TestInitializeAdvertisesCapabilities(t *testing.T) {
	t.Parallel()

	caps := result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))["capabilities"].(map[string]any)
	for _, k := range []string{"tools", "resources", "prompts"} {
		if _, ok := caps[k]; !ok {
			t.Errorf("capability %q missing: %v", k, caps)
		}
	}

	onlyPrompts := `{"mcpServer":{"prompts":[{"name":"p","messages":[{"role":"user","content":"x"}]}]}}`
	caps = result(t, rpc(t, onlyPrompts, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`))["capabilities"].(map[string]any)

	if _, ok := caps["tools"]; ok || len(caps) != 1 {
		t.Errorf("want only prompts, got %v", caps)
	}
}

func TestToolsListCarriesOptionalFields(t *testing.T) {
	t.Parallel()

	tools := result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))["tools"].([]any)
	first := tools[0].(map[string]any)

	if first["title"] != "Look up a user" || first["outputSchema"] == nil || first["annotations"] == nil {
		t.Errorf("optional fields missing: %v", first)
	}

	second := tools[1].(map[string]any)
	if _, ok := second["title"]; ok {
		t.Errorf("empty title must be omitted: %v", second)
	}

	if _, ok := second["outputSchema"]; ok {
		t.Errorf("absent outputSchema must be omitted: %v", second)
	}
}

func TestToolCallFillsStructuredContent(t *testing.T) {
	t.Parallel()

	res := result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"lookup"}}`))

	structured, ok := res["structuredContent"].(map[string]any)
	if !ok || structured["id"] != float64(7) {
		t.Errorf("want structuredContent {id:7}, got %v", res)
	}

	// A tool without outputSchema keeps its result as it was decoded.
	res = result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"plain"}}`))
	if _, ok := res["structuredContent"].(map[string]any); !ok {
		t.Errorf("an object mock is also exposed as structuredContent: %v", res)
	}
}

func TestEnsureStructuredLeavesOtherResults(t *testing.T) {
	t.Parallel()

	for name, res := range map[string]map[string]any{
		"has structured": {"structuredContent": map[string]any{"a": 1}},
		"not json":       {"content": []any{map[string]any{"type": "text", "text": "plain"}}},
		"not object":     {"content": []any{map[string]any{"type": "text", "text": "[1]"}}},
		"image":          {"content": []any{map[string]any{"type": "image"}, "junk"}},
	} {
		before := len(res)

		ensureStructured(res)

		if name == "has structured" && len(res) != before {
			t.Errorf("%s: result changed: %v", name, res)
		}

		if name != "has structured" {
			if _, ok := res["structuredContent"]; ok {
				t.Errorf("%s: unexpected structuredContent: %v", name, res)
			}
		}
	}
}

func TestResourcesList(t *testing.T) {
	t.Parallel()

	list := result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`))["resources"].([]any)
	if len(list) != 4 {
		t.Fatalf("want 4 resources, got %d", len(list))
	}

	first := list[0].(map[string]any)
	if first["uri"] != "file:///readme.md" || first["title"] != "Readme" || first["mimeType"] != "text/markdown" ||
		first["annotations"] == nil {
		t.Errorf("resource entry: %v", first)
	}

	if _, leaks := first["text"]; leaks {
		t.Errorf("listing must not carry content: %v", first)
	}

	if name := list[1].(map[string]any)["name"]; name != "file:///config.json" {
		t.Errorf("name defaults to the uri, got %v", name)
	}

	tpls := result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":2,"method":"resources/templates/list"}`))["resourceTemplates"].([]any)
	if len(tpls) != 3 || tpls[0].(map[string]any)["uriTemplate"] != "users://{id}/profile" {
		t.Errorf("templates: %v", tpls)
	}
}

func readContent(t *testing.T, uri string) map[string]any {
	t.Helper()

	req := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"` + uri + `"}}`
	contents := result(t, rpc(t, richManifest, req))["contents"].([]any)

	return contents[0].(map[string]any)
}

func TestResourcesRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		uri, field, want string
		mime             string
	}{
		{"file:///readme.md", "text", "# Hello", "text/markdown"},
		{"file:///config.json", "text", `{"debug":true}`, ""},
		{"file:///logo.png", "blob", "aGk=", "image/png"},
		{"file:///empty", "text", "", ""},
		{"users://42/profile", "text", "user 42", ""},
		{"docs://a/b/c.md", "text", `{"path":"a/b/c.md"}`, ""},
		{"search://items?q=x&limit=3", "text", "results", ""},
	}

	for _, tc := range cases {
		got := readContent(t, tc.uri)
		if got[tc.field] != tc.want || got["uri"] != tc.uri {
			t.Errorf("%s: got %v", tc.uri, got)
		}

		if tc.mime != "" && got["mimeType"] != tc.mime {
			t.Errorf("%s: mimeType %v", tc.uri, got["mimeType"])
		}
	}
}

func TestResourcesReadErrors(t *testing.T) {
	t.Parallel()

	resp := rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"nope://x"}}`)
	if errorCode(t, resp) != codeResourceNotFound {
		t.Errorf("unknown uri: %v", resp)
	}

	resp = rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{}}`)
	if errorCode(t, resp) != codeInvalidParams {
		t.Errorf("missing uri: %v", resp)
	}
}

func TestPromptsList(t *testing.T) {
	t.Parallel()

	list := result(t, rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`))["prompts"].([]any)
	p := list[0].(map[string]any)

	args := p["arguments"].([]any)
	if p["name"] != "review" || p["title"] != "Review code" || len(args) != 2 {
		t.Errorf("prompt entry: %v", p)
	}
}

func TestPromptsGet(t *testing.T) {
	t.Parallel()

	req := `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"review","arguments":{"code":"x := 1","lang":"Go"}}}`
	res := result(t, rpc(t, richManifest, req))

	if res["description"] != "Reviews code" {
		t.Errorf("description: %v", res)
	}

	msgs := res["messages"].([]any)
	first := msgs[0].(map[string]any)

	content := first["content"].(map[string]any)
	if first["role"] != "user" || content["type"] != "text" || content["text"] != "Review this Go code: x := 1" {
		t.Errorf("first message: %v", first)
	}

	second := msgs[1].(map[string]any)["content"].(map[string]any)["resource"].(map[string]any)
	if second["uri"] != "file:///Go" || second["text"] != "x := 1" {
		t.Errorf("object content must be substituted too: %v", second)
	}
}

func TestPromptsGetErrors(t *testing.T) {
	t.Parallel()

	for name, req := range map[string]string{
		"no name":          `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{}}`,
		"unknown":          `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"zzz"}}`,
		"missing required": `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"review","arguments":{"lang":"Go"}}}`,
	} {
		resp := rpc(t, richManifest, req)
		if errorCode(t, resp) != codeInvalidParams {
			t.Errorf("%s: %v", name, resp)
		}
	}
}

func TestPromptContentOtherJSON(t *testing.T) {
	t.Parallel()

	rep := replacer(map[string]string{"a": "b"})

	if got := promptContent(json.RawMessage(`{bad`), rep); got.(map[string]any)["text"] != "{bad" {
		t.Errorf("invalid JSON falls back to text: %v", got)
	}

	nested := promptContent(json.RawMessage(`{"list":["{{a}}",1,true]}`), rep).(map[string]any)
	if nested["list"].([]any)[0] != "b" || nested["list"].([]any)[1] != float64(1) {
		t.Errorf("nested substitute: %v", nested)
	}
}

func TestValidateResourcesAndPrompts(t *testing.T) {
	t.Parallel()

	bad := map[string]string{
		"no uri":         `{"mcpServer":{"resources":[{"name":"x"}]}}`,
		"duplicate uri":  `{"mcpServer":{"resources":[{"uri":"a"},{"uri":"a"}]}}`,
		"bad blob":       `{"mcpServer":{"resources":[{"uri":"a","blob":"***"}]}}`,
		"bad annotation": `{"mcpServer":{"resources":[{"uri":"a","annotations":[1]}]}}`,
		"tpl no var":     `{"mcpServer":{"resourceTemplates":[{"uriTemplate":"a://b"}]}}`,
		"tpl unbalanced": `{"mcpServer":{"resourceTemplates":[{"uriTemplate":"a://{b"}]}}`,
		"tpl empty var":  `{"mcpServer":{"resourceTemplates":[{"uriTemplate":"a://{}"}]}}`,
		"tpl duplicate": `{"mcpServer":{"resourceTemplates":[{"uriTemplate":"a://{b}"},
			{"uriTemplate":"a://{b}"}]}}`,
		"tpl vs resource": `{"mcpServer":{"resources":[{"uri":"a://{b}"}],"resourceTemplates":[{"uriTemplate":"a://{b}"}]}}`,
		"prompt no name":  `{"mcpServer":{"prompts":[{"messages":[{"role":"user","content":"x"}]}]}}`,
		"prompt dup": `{"mcpServer":{"prompts":[{"name":"p","messages":[{"role":"user","content":"x"}]},
			{"name":"p","messages":[{"role":"user","content":"x"}]}]}}`,
		"prompt dup arg": `{"mcpServer":{"prompts":[{"name":"p","arguments":[{"name":"a"},{"name":"a"}],
			"messages":[{"role":"user","content":"x"}]}]}}`,
		"prompt empty arg": `{"mcpServer":{"prompts":[{"name":"p","arguments":[{"name":""}],
			"messages":[{"role":"user","content":"x"}]}]}}`,
		"prompt no messages": `{"mcpServer":{"prompts":[{"name":"p"}]}}`,
		"prompt bad role":    `{"mcpServer":{"prompts":[{"name":"p","messages":[{"role":"system","content":"x"}]}]}}`,
		"prompt no content":  `{"mcpServer":{"prompts":[{"name":"p","messages":[{"role":"user"}]}]}}`,
		"outputSchema list":  `{"mcpServer":{"tools":[{"name":"t","outputSchema":[1]}]}}`,
		"annotations string": `{"mcpServer":{"tools":[{"name":"t","annotations":"x"}]}}`,
		"nothing at all":     `{"mcpServer":{"name":"x"}}`,
	}

	for name, raw := range bad {
		if res := NewEngine().Validate([]byte(raw)); res.Valid {
			t.Errorf("%s: want invalid", name)
		}
	}
}

func TestFlatLayoutWithResourcesOnly(t *testing.T) {
	t.Parallel()

	raw := `{"serverInfo":{"name":"f","version":"2"},"resources":[{"uri":"a://b","text":"x"}]}`

	res := NewEngine().Validate([]byte(raw))
	if !res.Valid || res.ResourceCount != 1 || res.ToolCount != 0 {
		t.Fatalf("flat resources-only manifest: %+v", res)
	}

	info := result(t, rpc(t, raw, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`))["serverInfo"].(map[string]any)
	if info["name"] != "f" {
		t.Errorf("serverInfo: %v", info)
	}
}

func TestCatalog(t *testing.T) {
	t.Parallel()

	cat, err := NewEngine().Catalog([]byte(richManifest))
	if err != nil {
		t.Fatal(err)
	}

	if len(cat.Tools) != 2 || len(cat.Resources) != 4 || len(cat.Templates) != 3 || len(cat.Prompts) != 1 {
		t.Fatalf("catalog sizes: %+v", cat)
	}

	if cat.Resources[0].Title != "Readme" || cat.Templates[0].URITemplate != "users://{id}/profile" ||
		cat.Prompts[0].Arguments[0].Name != "code" || cat.Tools[0].Title == "" {
		t.Errorf("catalog content: %+v", cat)
	}

	_, err = NewEngine().Catalog([]byte(`{`))
	if err == nil {
		t.Error("want an error for broken JSON")
	}
}

func TestCompileTemplate(t *testing.T) {
	t.Parallel()

	tpl, err := compileTemplate("a://{x}/b/{y}")
	if err != nil {
		t.Fatal(err)
	}

	if v, ok := tpl.match("a://1/b/2"); !ok || v["x"] != "1" || v["y"] != "2" {
		t.Errorf("match: %v %v", v, ok)
	}

	if _, ok := tpl.match("a://1/c/2"); ok {
		t.Error("must not match a different literal")
	}

	if _, ok := tpl.match("a://1/b/2/3"); ok {
		t.Error("{y} must not span slashes")
	}

	if _, err := compileTemplate("{a}}"); err == nil {
		t.Error("unbalanced braces must fail")
	}
}

func TestUnsupportedMethodsStillNotFound(t *testing.T) {
	t.Parallel()

	resp := rpc(t, richManifest, `{"jsonrpc":"2.0","id":1,"method":"resources/subscribe","params":{"uri":"x"}}`)
	if errorCode(t, resp) != codeMethodNotFound || !strings.Contains(resp["error"].(map[string]any)["message"].(string), "subscribe") {
		t.Errorf("subscribe is not implemented: %v", resp)
	}

	_ = usecase.MCPSupportedProtocolVersions
}
