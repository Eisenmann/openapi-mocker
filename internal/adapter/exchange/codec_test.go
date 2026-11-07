package exchange_test

import (
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/exchange"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const postman = `{"info":{"name":"Shop","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
"item":[{"name":"Users","item":[
 {"name":"Get","request":{"method":"GET","url":{"raw":"{{baseUrl}}/users/:id?expand=1","path":["users",":id"],"query":[{"key":"expand","value":"1"},{"key":"off","disabled":true}]}},
  "response":[{"name":"ok","code":200,"header":[{"key":"Content-Type","value":"application/json"}],"body":"{\"id\":1}"},
              {"name":"xml","code":200,"_postman_previewlanguage":"xml","body":"<a/>"},
              {"name":"text","code":500,"body":"oops"},{"name":"empty","code":0,"body":""}]},
 {"name":"Create","request":{"method":"post","url":"{{baseUrl}}/users","body":{"mode":"raw","raw":"{\"n\":1}"}}}]},
 {"name":"Bare","request":"https://x.test/health"},
 {"name":"Parts","request":{"method":"GET","url":{"path":["a",{"value":"b"},"{{c}}"]}}},
 {"name":"Broken","request":{"method":"GET"}}]}`

func TestDetectImport(t *testing.T) {
	t.Parallel()

	c := exchange.New()

	for name, tc := range map[string]struct{ in, want string }{
		"bundle":     {`{"format":"openapi-mocker.bundle"}`, usecase.KindBundle},
		"har":        {`{"log":{"entries":[]}}`, usecase.KindHAR},
		"insomnia4":  {`{"_type":"export","resources":[]}`, usecase.KindInsomnia},
		"insomnia5":  {"type: collection.insomnia.rest/5.0\nname: x\n", usecase.KindInsomnia},
		"postman":    {postman, usecase.KindPostman},
		"postman id": {`{"info":{"_postman_id":"1"},"item":[]}`, usecase.KindPostman},
		"openapi":    {`{"openapi":"3.0.0"}`, usecase.KindOpenAPI},
		"swagger":    {"swagger: '2.0'\n", usecase.KindOpenAPI},
	} {
		got, err := c.DetectImport([]byte(tc.in))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", name, got, err, tc.want)
		}
	}

	for _, bad := range []string{``, `nope`, `{}`, `[1]`, `{"info":{},"item":[]}`} {
		if _, err := c.DetectImport([]byte(bad)); err == nil {
			t.Errorf("%q must not be recognised", bad)
		}
	}
}

func TestParseImport_Postman(t *testing.T) {
	t.Parallel()

	api, err := exchange.New().ParseImport(usecase.KindPostman, []byte(postman))
	if err != nil {
		t.Fatal(err)
	}

	if api.Name != "Shop" {
		t.Errorf("name %q", api.Name)
	}

	by := map[string]usecase.ImportedEndpoint{}
	for _, ep := range api.Endpoints {
		by[ep.Method+" "+ep.Path] = ep
	}

	get := by["GET /users/{id}"]
	if len(get.Query) != 1 || get.Query[0] != "expand" || len(get.Responses) != 3 {
		t.Fatalf("GET /users/{id}: %+v", get)
	}

	types := map[string]string{}
	for _, r := range get.Responses {
		types[r.ContentType] = r.Body
	}

	if types["application/json"] != `{"id":1}` || types["application/xml"] != "<a/>" || types["text/plain"] != "oops" {
		t.Errorf("response content types: %v", types)
	}

	if post := by["POST /users"]; post.Request == nil || post.Request.ContentType != "application/json" {
		t.Errorf("POST body: %+v", post)
	}

	for _, want := range []string{"GET /health", "GET /a/b/{c}"} {
		if _, ok := by[want]; !ok {
			t.Errorf("missing endpoint %s in %v", want, by)
		}
	}

	if len(api.Warnings) != 1 {
		t.Errorf("the request without a URL must be a warning: %v", api.Warnings)
	}
}

func TestParseImport_HAR(t *testing.T) {
	t.Parallel()

	har := `{"log":{"entries":[
 {"request":{"method":"GET","url":"https://a.test/v1/orders/123e4567-e89b-12d3-a456-426614174000?x=1","queryString":[]},
  "response":{"status":200,"headers":[{"name":"Content-Type","value":"application/json"},{"name":"Authorization","value":"s"}],"content":{"mimeType":"application/json","text":"eyJhIjoxfQ==","encoding":"base64"}}},
 {"request":{"method":"GET","url":"https://b.test/v1/orders/9"},"response":{"status":200,"headers":[],"content":{"mimeType":"text/plain","text":"hi"}}},
 {"request":{"method":"GET","url":"https://b.test/v1/orders/9"},"response":{"status":304,"headers":[],"content":{}}},
 {"request":{"method":"GET","url":"::bad"},"response":{"status":200,"headers":[],"content":{}}}
]}}`

	api, err := exchange.New().ParseImport(usecase.KindHAR, []byte(har))
	if err != nil {
		t.Fatal(err)
	}

	if len(api.Endpoints) != 1 || api.Endpoints[0].PathTypes["orderId"] != "string" || len(api.Endpoints[0].Responses) != 2 {
		t.Fatalf("a UUID and a number in one position must give one string parameter: %+v", api.Endpoints)
	}

	var multi bool

	for _, w := range api.Warnings {
		multi = multi || strings.Contains(w, "host")
	}

	if !multi {
		t.Errorf("several hosts must be reported: %v", api.Warnings)
	}

	if _, err := exchange.New().ParseImport(usecase.KindHAR, []byte(`not json`)); err == nil {
		t.Error("a broken HAR must fail")
	}

	if _, err := exchange.New().ParseImport("nonsense", nil); err == nil {
		t.Error("unknown kind must fail")
	}
}

func TestParseImport_Insomnia(t *testing.T) {
	t.Parallel()

	v4 := `{"_type":"export","resources":[{"_id":"w","_type":"workspace","name":"W"},
 {"_id":"r","_type":"request","parentId":"w","name":"List","method":"get","url":"{{ _.base }}/items/:id","parameters":[{"name":"q","value":"1"},{"name":"d","disabled":true}],
  "body":{"mimeType":"application/json","text":"{}"}},
 {"_id":"e","_type":"environment","name":"env"}]}`

	api, err := exchange.New().ParseImport(usecase.KindInsomnia, []byte(v4))
	if err != nil || len(api.Endpoints) != 1 || api.Endpoints[0].Path != "/items/{id}" ||
		len(api.Endpoints[0].Query) != 1 || api.Endpoints[0].Request == nil {
		t.Fatalf("v4: %+v %v", api, err)
	}

	v5 := "type: collection.insomnia.rest/5.0\nname: V5\ncollection:\n  - name: F\n    children:\n      - url: \"{{ _.u }}/a\"\n        method: POST\n        body: {text: \"x\"}\n"

	api, err = exchange.New().ParseImport(usecase.KindInsomnia, []byte(v5))
	if err != nil || api.Name != "V5" || len(api.Endpoints) != 1 || api.Endpoints[0].Method != "POST" {
		t.Fatalf("v5: %+v %v", api, err)
	}

	empty, err := exchange.New().ParseImport(usecase.KindInsomnia, []byte(`{"_type":"export","resources":[]}`))
	if err != nil || len(empty.Endpoints) != 0 {
		t.Errorf("an export without requests parses to nothing: %+v %v", empty, err)
	}
}

func exportData() *usecase.ExportData {
	return &usecase.ExportData{
		Name: "Shop", BaseURL: "http://localhost:8080/mock/p1",
		Endpoints: []usecase.Endpoint{
			{Path: "/users/{id}", Method: "GET", Summary: "Get user"},
			{Path: "/users", Method: "POST", Summary: ""},
		},
		Mocks: []*domain.MockRule{
			{
				Path: "/users/{id}", Method: "GET", Scenario: "default", StatusCode: 200, ContentType: "application/json",
				Headers: map[string]string{"X-A": "1"}, Body: `{"id":1}`,
			},
			{Path: "/users/{id}", Method: "GET", Scenario: "status-404", StatusCode: 404, ContentType: "application/json", Body: `{"e":1}`},
			{Path: "/users/{id}", Method: "GET", Scenario: "faulty", StatusCode: 200, Fault: &domain.Fault{}},
			{Path: "/users", Method: "POST", Scenario: "default", StatusCode: 201, ContentType: "text/plain", Body: "ok"},
		},
	}
}

func TestExportCollection_RoundTrips(t *testing.T) {
	t.Parallel()

	c := exchange.New()

	for _, format := range []string{usecase.CollectionPostman, usecase.CollectionInsomnia} {
		out, err := c.ExportCollection(format, exportData())
		if err != nil || !strings.Contains(string(out), "/users") {
			t.Fatalf("%s export: %v\n%s", format, err, out)
		}

		kind, err := c.DetectImport(out)
		if err != nil {
			t.Fatalf("%s: exported file not recognised: %v", format, err)
		}

		api, err := c.ParseImport(kind, out)
		if err != nil || len(api.Endpoints) != 2 {
			t.Fatalf("%s round trip: %+v %v", format, api, err)
		}
	}

	if _, err := c.ExportCollection("csv", exportData()); err == nil {
		t.Error("unknown collection format must fail")
	}
}
