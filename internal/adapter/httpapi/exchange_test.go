package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/exchange"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// exchangeEnv is the full import/export stack over a real store.
type exchangeEnv struct {
	h     http.Handler
	store *jsonstore.Store
}

func newExchangeEnv(t *testing.T) *exchangeEnv {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := openapi.NewEngine()
	projects := usecase.NewProjectService(store)
	contracts := usecase.NewContractService(store, store, engine, nil).WithConverter(engine)
	mocks := usecase.NewMockService(store, store, store, engine, nil)

	return &exchangeEnv{store: store, h: httpapi.NewRouter(&httpapi.Services{
		Projects:    projects,
		Contracts:   contracts,
		Mocks:       mocks,
		Exchange:    usecase.NewExchangeService(projects, store, contracts, mocks, exchange.New()),
		MockServing: usecase.NewMockServingService(store, store, store, engine, usecase.WithProjects(store)),
		Logs:        usecase.NewLogService(store),
	})}
}

func (e *exchangeEnv) importFile(t *testing.T, body, query string) (*httptest.ResponseRecorder, usecase.ImportResult) {
	t.Helper()

	rec := doRequest(t, e.h, http.MethodPost, "/api/import"+query, strings.NewReader(body), nil)

	var res usecase.ImportResult
	if rec.Code == http.StatusCreated {
		decodeJSON(t, rec, &res)
	}

	return rec, res
}

func (e *exchangeEnv) mock(t *testing.T, projectID, path string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, e.h, http.MethodGet, "/mock/"+projectID+path, nil, nil)
}

const harSample = `{"log":{"version":"1.2","entries":[
 {"request":{"method":"GET","url":"https://api.shop.test/v1/users/42?verbose=1","queryString":[{"name":"verbose","value":"1"}]},
  "response":{"status":200,"headers":[{"name":"Content-Type","value":"application/json"},{"name":"Set-Cookie","value":"s=1"},{"name":"X-Trace","value":"t1"}],
   "content":{"mimeType":"application/json","text":"{\"id\":42,\"name\":\"Ada {{x}}\",\"email\":null}"}}},
 {"request":{"method":"GET","url":"https://api.shop.test/v1/users/7"},
  "response":{"status":200,"headers":[],"content":{"mimeType":"application/json","text":"{\"id\":7,\"name\":\"Bob\",\"email\":\"b@x.y\"}"}}},
 {"request":{"method":"GET","url":"https://api.shop.test/v1/users/9"},
  "response":{"status":404,"headers":[],"content":{"mimeType":"application/json","text":"{\"error\":\"not found\"}"}}},
 {"request":{"method":"POST","url":"https://api.shop.test/v1/users","postData":{"mimeType":"application/json","text":"{\"name\":\"Cy\"}"}},
  "response":{"status":201,"headers":[],"content":{"mimeType":"application/json","text":"{\"id\":1,\"name\":\"Cy\"}"}}},
 {"request":{"method":"GET","url":"https://api.shop.test/static/app.js"},
  "response":{"status":200,"headers":[],"content":{"mimeType":"application/javascript","text":"var a=1"}}},
 {"request":{"method":"OPTIONS","url":"https://api.shop.test/v1/users"},"response":{"status":204,"headers":[],"content":{"mimeType":""}}}
]}}`

func TestImport_HARBuildsContractAndMocks(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)

	rec, res := env.importFile(t, harSample, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}

	if res.Kind != usecase.KindHAR || res.Endpoints != 2 || res.Mocks != 3 {
		t.Fatalf("result: %+v", res)
	}

	id := res.Project.ID

	got := env.mock(t, id, "/v1/users/1234")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"Ada {{x}}"`) {
		t.Errorf("the first example answers verbatim: %d %s", got.Code, got.Body.String())
	}

	if got.Header().Get("X-Upstream") != "" || got.Header().Get("Set-Cookie") != "" {
		t.Errorf("cookies must not be imported: %v", got.Header())
	}

	if got.Header().Get("X-Trace") != "t1" {
		t.Errorf("ordinary headers are kept: %v", got.Header())
	}

	// A named scenario serves the 404 example.
	nf := doRequest(t, env.h, http.MethodGet, "/mock/"+id+"/v1/users/1", nil, map[string]string{"X-Mock-Scenario": "status-404"})
	if nf.Code != http.StatusNotFound || !strings.Contains(nf.Body.String(), "not found") {
		t.Errorf("status-404 scenario: %d %s", nf.Code, nf.Body.String())
	}

	if post := doRequest(t, env.h, http.MethodPost, "/mock/"+id+"/v1/users", strings.NewReader(`{}`), nil); post.Code != http.StatusCreated {
		t.Errorf("POST example: %d %s", post.Code, post.Body.String())
	}

	// The contract has the templated path, the integer parameter and the query.
	c, err := env.store.GetActive(id)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{`"/v1/users/{userId}"`, `"integer"`, `"verbose"`, `"nullable": true`} {
		if !strings.Contains(c.Raw, want) {
			t.Errorf("contract lacks %s:\n%s", want, c.Raw)
		}
	}

	if strings.Contains(res.Project.Name, "static") || len(res.Warnings) == 0 {
		t.Errorf("project %q warnings %v", res.Project.Name, res.Warnings)
	}
}

const postmanSample = `{
 "info":{"name":"Shop","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json","_postman_id":"x"},
 "item":[
  {"name":"Users","item":[
    {"name":"Get user","request":{"method":"GET","url":{"raw":"{{baseUrl}}/users/:id?expand=1","path":["users",":id"],"query":[{"key":"expand","value":"1"}]}},
     "response":[{"name":"ok","code":200,"header":[{"key":"Content-Type","value":"application/json"}],"body":"{\"id\":1,\"name\":\"Ada\"}"},
                 {"name":"missing","code":404,"header":[],"body":"{\"error\":\"nope\"}"}]},
    {"name":"Create","request":{"method":"POST","url":"{{baseUrl}}/users","body":{"mode":"raw","raw":"{\"name\":\"x\"}","options":{"raw":{"language":"json"}}}}}
  ]},
  {"name":"Health","request":"{{baseUrl}}/health"}
 ]}`

func TestImport_PostmanCollection(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)

	rec, res := env.importFile(t, postmanSample, "?name=My+Shop")
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}

	if res.Kind != usecase.KindPostman || res.Endpoints != 3 || res.Mocks != 2 || res.Project.Name != "My Shop" {
		t.Fatalf("result: %+v", res)
	}

	if got := env.mock(t, res.Project.ID, "/users/5"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Ada") {
		t.Errorf("example response: %d %s", got.Code, got.Body.String())
	}

	// Endpoints without a saved example are answered from the contract.
	if got := env.mock(t, res.Project.ID, "/health"); got.Code != http.StatusOK {
		t.Errorf("/health: %d", got.Code)
	}
}

const insomniaV4 = `{"_type":"export","__export_format":4,"resources":[
 {"_id":"wrk_1","_type":"workspace","name":"Shop API"},
 {"_id":"fld_1","_type":"request_group","parentId":"wrk_1","name":"Users"},
 {"_id":"req_1","_type":"request","parentId":"fld_1","name":"List","method":"GET","url":"{{ _.base_url }}/users","parameters":[{"name":"page","value":"1"}]},
 {"_id":"req_2","_type":"request","parentId":"fld_1","name":"Update","method":"PUT","url":"{{ _.base_url }}/users/{{ _.id }}",
  "body":{"mimeType":"application/json","text":"{\"name\":\"x\"}"}}
]}`

const insomniaV5 = `type: collection.insomnia.rest/5.0
name: Shop v5
collection:
  - name: Users
    children:
      - url: "{{ _.baseUrl }}/orders/:orderId"
        name: Get order
        method: GET
`

func TestImport_InsomniaV4AndV5(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)

	rec, res := env.importFile(t, insomniaV4, "")
	if rec.Code != http.StatusCreated || res.Kind != usecase.KindInsomnia || res.Endpoints != 2 || res.Project.Name != "Shop API" {
		t.Fatalf("v4: %d %+v %s", rec.Code, res, rec.Body.String())
	}

	c, _ := env.store.GetActive(res.Project.ID)
	if !strings.Contains(c.Raw, `"/users/{id}"`) || !strings.Contains(c.Raw, `"page"`) {
		t.Errorf("v4 contract:\n%s", c.Raw)
	}

	rec, res = env.importFile(t, insomniaV5, "")
	if rec.Code != http.StatusCreated || res.Endpoints != 1 || res.Project.Name != "Shop v5" {
		t.Fatalf("v5: %d %+v %s", rec.Code, res, rec.Body.String())
	}

	c, _ = env.store.GetActive(res.Project.ID)
	if !strings.Contains(c.Raw, `"/orders/{orderId}"`) {
		t.Errorf("v5 contract:\n%s", c.Raw)
	}
}

const swagger2Sample = `swagger: "2.0"
info: {title: Pets, version: "1"}
basePath: /api
paths:
  /pets/{id}:
    get:
      parameters:
        - {name: id, in: path, required: true, type: integer}
      produces: [application/json]
      responses:
        '200':
          description: OK
          schema: {type: object, properties: {id: {type: integer}, name: {type: string}}}
`

const openapi31Sample = `{"openapi":"3.1.0","info":{"title":"T","version":"1","summary":"s"},"paths":{"/x":{"get":{"responses":{"200":{"description":"OK",
 "content":{"application/json":{"schema":{"type":["object","null"],"properties":{"n":{"type":["string","null"]},"c":{"const":"a"}},"examples":[{"n":"q"}]}}}}}}}}}`

func TestImport_SwaggerAndOpenAPI31AreConverted(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)

	for name, tc := range map[string]struct{ body, wantPath string }{
		"swagger 2":   {swagger2Sample, "/pets/{id}"},
		"openapi 3.1": {openapi31Sample, "/x"},
	} {
		rec, res := env.importFile(t, tc.body, "")
		if rec.Code != http.StatusCreated || res.Kind != usecase.KindOpenAPI || res.Endpoints != 1 {
			t.Fatalf("%s: %d %+v %s", name, rec.Code, res, rec.Body.String())
		}

		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "converted") {
			t.Errorf("%s: want a conversion note, got %v", name, res.Warnings)
		}

		c, _ := env.store.GetActive(res.Project.ID)
		if !strings.Contains(c.Raw, `"openapi": "3.0.3"`) || !strings.Contains(c.Raw, tc.wantPath) {
			t.Errorf("%s: converted contract:\n%s", name, c.Raw)
		}

		if got := env.mock(t, res.Project.ID, strings.NewReplacer("{id}", "3").Replace(tc.wantPath)); got.Code != http.StatusOK {
			t.Errorf("%s: mock %d %s", name, got.Code, got.Body.String())
		}
	}

	// Pasting such a contract into the Contract tab works the same way.
	p, _ := usecase.NewProjectService(env.store).Create("manual", "")
	rec := call(t, env.h, http.MethodPost, "/api/projects/"+p.ID+"/contract/validate", `{"raw":`+jsonString(swagger2Sample)+`}`)

	var v usecase.ValidationResult
	decodeJSON(t, rec, &v)

	if !v.Valid || v.Note == "" {
		t.Errorf("validate must convert and say so: %+v", v)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)

	return string(b)
}

func TestExport_BundleRoundTrip(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)
	_, res := env.importFile(t, postmanSample, "?name=Shop")
	id := res.Project.ID

	if _, err := usecase.NewProjectService(env.store).SetValidationMode(id, domain.ValidationWarn); err != nil {
		t.Fatal(err)
	}

	env.store.AddVersion(id, "yaml", "openapi: 3.0.0\ninfo: {title: v2, version: '2'}\npaths: {}\n", "manual")

	rec := doRequest(t, env.h, http.MethodGet, "/api/projects/"+id+"/export?history=1", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "shop.openapi-mocker.json") {
		t.Fatalf("export: %d %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}

	var bundle usecase.Bundle
	decodeJSON(t, rec, &bundle)

	if bundle.Format != usecase.BundleFormat || len(bundle.Contracts) != 2 || len(bundle.Mocks) != 2 {
		t.Fatalf("bundle: %+v", bundle)
	}

	if bundle.Mocks[0].ProjectID != "" || bundle.Mocks[0].ID != "" {
		t.Error("ids must not travel in a bundle")
	}

	latest := doRequest(t, env.h, http.MethodGet, "/api/projects/"+id+"/export", nil, nil)

	var short usecase.Bundle
	decodeJSON(t, latest, &short)

	if len(short.Contracts) != 1 {
		t.Errorf("without history only the active version goes in, got %d", len(short.Contracts))
	}

	// Import the full bundle again as a new project.
	again, res2 := env.importFile(t, rec.Body.String(), "?name=Copy")
	if again.Code != http.StatusCreated || res2.Kind != usecase.KindBundle || res2.Contracts != 2 || res2.Mocks != 2 {
		t.Fatalf("re-import: %d %+v %s", again.Code, res2, again.Body.String())
	}

	if res2.Project.ID == id || res2.Project.Name != "Copy" || res2.Project.ValidationMode != domain.ValidationWarn {
		t.Errorf("copy: %+v", res2.Project)
	}

	if n := len(env.store.ListVersions(res2.Project.ID)); n != 2 {
		t.Errorf("history not restored: %d versions", n)
	}
}

func TestExport_PostmanAndInsomniaRoundTrip(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)
	_, res := env.importFile(t, postmanSample, "?name=Shop")
	id := res.Project.ID

	postman := doRequest(t, env.h, http.MethodGet, "/api/projects/"+id+"/export?format=postman", nil, nil)
	if postman.Code != http.StatusOK || !strings.Contains(postman.Body.String(), "{{baseUrl}}/users/:id") ||
		!strings.Contains(postman.Body.String(), "/mock/"+id) {
		t.Fatalf("postman export: %d\n%s", postman.Code, postman.Body.String())
	}

	// What we export we can import again, examples included.
	back, res2 := env.importFile(t, postman.Body.String(), "")
	if back.Code != http.StatusCreated || res2.Kind != usecase.KindPostman || res2.Endpoints != 3 || res2.Mocks != 2 {
		t.Fatalf("postman round trip: %d %+v %s", back.Code, res2, back.Body.String())
	}

	insomnia := doRequest(t, env.h, http.MethodGet, "/api/projects/"+id+"/export?format=insomnia", nil, nil)
	if insomnia.Code != http.StatusOK {
		t.Fatalf("insomnia export: %d", insomnia.Code)
	}

	back, res3 := env.importFile(t, insomnia.Body.String(), "")
	if back.Code != http.StatusCreated || res3.Kind != usecase.KindInsomnia || res3.Endpoints != 3 {
		t.Fatalf("insomnia round trip: %d %+v %s", back.Code, res3, back.Body.String())
	}

	if bad := doRequest(t, env.h, http.MethodGet, "/api/projects/"+id+"/export?format=csv", nil, nil); bad.Code != http.StatusBadRequest {
		t.Errorf("unknown export format: %d", bad.Code)
	}
}

func TestExport_ContractAsOpenAPI31AndSwagger2(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)
	p, _ := usecase.NewProjectService(env.store).Create("conv", "")
	env.store.AddVersion(p.ID, "yaml", `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /u/{id}:
    parameters: [{name: id, in: path, required: true, schema: {type: integer}}]
    get:
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema: {type: object, properties: {name: {type: string, nullable: true}}}
`, "manual")

	r31 := doRequest(t, env.h, http.MethodGet, "/api/projects/"+p.ID+"/contract/export?format=openapi31&as=json", nil, nil)
	if r31.Code != http.StatusOK || !strings.Contains(r31.Body.String(), `"3.1.0"`) || strings.Contains(r31.Body.String(), "nullable") ||
		!strings.Contains(r31.Body.String(), `"null"`) {
		t.Fatalf("3.1 export: %d\n%s", r31.Code, r31.Body.String())
	}

	sw := doRequest(t, env.h, http.MethodGet, "/api/projects/"+p.ID+"/contract/export?format=swagger2&as=yaml", nil, nil)
	if sw.Code != http.StatusOK || !strings.Contains(sw.Body.String(), "swagger: ") || !strings.Contains(sw.Body.String(), "/u/{id}") {
		t.Fatalf("swagger export: %d\n%s", sw.Code, sw.Body.String())
	}

	// And both convert back to something that can be published.
	for _, body := range []string{r31.Body.String(), sw.Body.String()} {
		q := call(t, env.h, http.MethodPost, "/api/projects/"+p.ID+"/contract/validate", `{"raw":`+jsonString(body)+`}`)

		var v usecase.ValidationResult
		decodeJSON(t, q, &v)

		if !v.Valid {
			t.Errorf("the exported contract must validate again: %+v", v)
		}
	}

	if bad := doRequest(t, env.h, http.MethodGet, "/api/projects/"+p.ID+"/contract/export?format=raml", nil, nil); bad.Code != http.StatusBadRequest {
		t.Errorf("unknown contract format: %d", bad.Code)
	}
}

func TestImport_RejectsUnknownAndBrokenFiles(t *testing.T) {
	t.Parallel()

	env := newExchangeEnv(t)

	for name, body := range map[string]string{
		"not a known format": `{"hello":"world"}`,
		"plain text":         `just some text`,
		"empty":              ``,
		"empty har":          `{"log":{"entries":[]}}`,
		"bad bundle version": `{"format":"openapi-mocker.bundle","version":99}`,
		"bad bundle contract": `{"format":"openapi-mocker.bundle","version":1,"project":{"name":"x"},
			"contracts":[{"format":"yaml","raw":"openapi: 3.0.0\ninfo: {}\n"}],"mocks":[]}`,
	} {
		rec, _ := env.importFile(t, body, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", name, rec.Code, rec.Body.String())
		}
	}

	if n := len(env.store.List()); n != 0 {
		t.Errorf("a failed import must leave no project behind, found %d", n)
	}
}
