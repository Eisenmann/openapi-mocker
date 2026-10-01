package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const templatingMCP = `{"mcpServer":{"name":"demo","tools":[{"name":"greet",
	"inputSchema":{"type":"object"},
	"mockResponses":{"default":{"content":[{"type":"text","text":"Hello {{request.body.name}} #{{counter}}"}],
		"structuredContent":{"id":"{{counter hits}}","who":"{{request.body.name}}","uid":"{{uuid}}"}}}}]}}`

func newTemplatingRouter(t *testing.T, format, raw string, rules ...*domain.MockRule) (h http.Handler, projectID string) {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	mocks := newMemMockRepo()
	logs := newMemLogRepo()

	p := projects.Create("t", "")
	contracts.AddVersion(p.ID, format, raw, "manual")

	for _, r := range rules {
		r.ProjectID = p.ID
		mocks.CreateMock(r)
	}

	svc := httpapi.Services{
		Projects:    usecase.NewProjectService(projects),
		Mocks:       usecase.NewMockService(mocks, contracts, nil, openapi.NewEngine(), nil),
		MockServing: usecase.NewMockServingService(contracts, mocks, logs, openapi.NewEngine()),
		MCPServing:  usecase.NewMCPServingService(contracts, logs, mcp.NewEngine()),
	}

	return httpapi.NewRouter(&svc), p.ID
}

func TestRouter_RESTResponseTemplating(t *testing.T) {
	t.Parallel()

	rule := &domain.MockRule{
		Path: "/users/{id}", Method: "PUT", StatusCode: 200,
		Headers: map[string]string{"X-Request-Id": "{{uuid}}"},
		Body:    `{"id":{{path.id}},"name":"{{request.body.name}}","page":"{{request.query.page}}","n":{{counter}}}`,
	}
	h, id := newTemplatingRouter(t, "yaml", stateOpenAPI, rule)

	url := "/mock/" + id + "/users/9?page=3"
	rec := call(t, h, http.MethodPut, url, `{"name":"Zoe \"Z\""}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	decodeJSON(t, rec, &got)

	if got["id"] != float64(9) || got["name"] != `Zoe "Z"` || got["page"] != "3" || got["n"] != float64(1) {
		t.Errorf("body not templated: %v", got)
	}

	if len(rec.Header().Get("X-Request-Id")) != 36 {
		t.Errorf("header not templated: %q", rec.Header().Get("X-Request-Id"))
	}

	rec = call(t, h, http.MethodPut, url, `{"name":"b"}`)
	if !strings.Contains(rec.Body.String(), `"n":2`) {
		t.Errorf("counter must advance: %s", rec.Body.String())
	}
}

func TestRouter_RuleWithPlaceholdersCanBeSaved(t *testing.T) {
	t.Parallel()

	h, id := newTemplatingRouter(t, "yaml", stateOpenAPI)

	body := `{"path":"/users/{id}","method":"GET","statusCode":200,"body":"{\"id\":{{path.id}},\"n\":\"{{faker.name}}\"}"}`
	if rec := call(t, h, http.MethodPost, "/api/projects/"+id+"/mocks", body); rec.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}

	rec := call(t, h, http.MethodGet, "/mock/"+id+"/users/5", "")
	if !strings.Contains(rec.Body.String(), `"id":5`) || strings.Contains(rec.Body.String(), "{{") {
		t.Errorf("saved rule not rendered: %s", rec.Body.String())
	}
}

func TestRouter_MCPResponseTemplating(t *testing.T) {
	t.Parallel()

	h, id := newTemplatingRouter(t, "mcp", templatingMCP)

	call1 := func() map[string]any {
		rec := call(t, h, http.MethodPost, "/mock/"+id+"/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"greet","arguments":{"name":"Ann"}}}`)

		var out struct {
			Result map[string]any `json:"result"`
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad response %s: %v", rec.Body.String(), err)
		}

		return out.Result
	}

	r := call1()

	text := r["content"].([]any)[0].(map[string]any)["text"]
	if text != "Hello Ann #1" {
		t.Errorf("text: %v", text)
	}

	sc := r["structuredContent"].(map[string]any)
	if sc["id"] != float64(1) || sc["who"] != "Ann" || len(sc["uid"].(string)) != 36 {
		t.Errorf("structuredContent: %v", sc)
	}

	if sc2 := call1()["structuredContent"].(map[string]any); sc2["id"] != float64(2) {
		t.Errorf("counter must advance per call: %v", sc2)
	}
}
