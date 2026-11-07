package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const endpointAuthConfig = `{"mode":"contract","credentials":[
 {"value":"good"},{"value":"old","scenario":"expired"},{"value":"nope","scenario":"forbidden"}],
 "clients":[{"id":"svc","secret":"s"}]}`

func newEndpointRouter(t *testing.T, format, raw string) (http.Handler, string) {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := openapi.NewEngine()
	projects := usecase.NewProjectService(store)
	auth := usecase.NewAuthService(store, engine, usecase.WithAuthLogs(store))

	h := httpapi.NewRouter(&httpapi.Services{
		Projects: projects,
		MockServing: usecase.NewMockServingService(
			store, store, store, engine, usecase.WithProjects(store), usecase.WithAuth(auth)),
		GraphQLServing: usecase.NewGraphQLServingService(store, store, graphql.NewEngine()),
		MCPServing:     usecase.NewMCPServingService(store, store, mcp.NewEngine(), usecase.WithProjects(store)),
		Auth:           auth,
		Logs:           usecase.NewLogService(store),
	})

	p, err := projects.Create("ep", "")
	if err != nil {
		t.Fatal(err)
	}

	store.AddVersion(p.ID, format, raw, "test")

	if rec := call(t, h, http.MethodPut, "/api/projects/"+p.ID+"/auth", endpointAuthConfig); rec.Code != http.StatusOK {
		t.Fatalf("set auth: %d %s", rec.Code, rec.Body.String())
	}

	return h, p.ID
}

func TestAuth_GraphQLEndpoint(t *testing.T) {
	t.Parallel()

	h, id := newEndpointRouter(t, "graphql", "type Query { ping: String }")
	post := func(headers map[string]string) *httptest.ResponseRecorder {
		h2 := map[string]string{"Content-Type": "application/json"}
		for k, v := range headers {
			h2[k] = v
		}

		return doRequest(t, h, http.MethodPost, "/mock/"+id+"/graphql", strings.NewReader(`{"query":"{ ping }"}`), h2)
	}

	if rec := post(nil); rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("no credentials: %d %v", rec.Code, rec.Header())
	}

	for tok, want := range map[string]int{"good": 200, "old": 401, "nope": 403, "other": 401} {
		if rec := post(bearer(tok)); rec.Code != want {
			t.Errorf("graphql with %s: want %d, got %d %s", tok, want, rec.Code, rec.Body.String())
		}
	}

	logs := call(t, h, http.MethodGet, "/api/projects/"+id+"/logs", "")
	if !strings.Contains(logs.Body.String(), `"source":"auth"`) {
		t.Errorf("rejections must be logged: %s", logs.Body.String())
	}
}

func TestAuth_MCPEndpointAndTokenFlow(t *testing.T) {
	t.Parallel()

	h, id := newEndpointRouter(t, "mcp", validationMCP)
	rpc := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	post := func(headers map[string]string) *httptest.ResponseRecorder {
		return doRequest(t, h, http.MethodPost, "/mock/"+id+"/mcp", strings.NewReader(rpc), headers)
	}

	if rec := post(nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("mcp without credentials: %d", rec.Code)
	}

	if rec := post(bearer("good")); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "echo") {
		t.Errorf("mcp with a token: %d %s", rec.Code, rec.Body.String())
	}

	// The token endpoint works for non-REST projects, and its tokens open the endpoint.
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"svc"}, "client_secret": {"s"}}
	tok := doRequest(t, h, http.MethodPost, "/mock/"+id+"/oauth/token", strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})

	var out struct {
		AccessToken string `json:"access_token"`
	}

	decodeJSON(t, tok, &out)

	if rec := post(bearer(out.AccessToken)); tok.Code != http.StatusOK || rec.Code != http.StatusOK {
		t.Errorf("issued token on mcp: token %d, call %d", tok.Code, rec.Code)
	}
}
