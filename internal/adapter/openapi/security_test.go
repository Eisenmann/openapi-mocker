package openapi_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const securityDoc = `openapi: 3.0.3
info: {title: T, version: "1"}
security: [{bearer: []}]
components:
  securitySchemes:
    bearer: {type: http, scheme: bearer}
    key: {type: apiKey, in: Header, name: X-Key}
    basic: {type: http, scheme: basic}
    oauth: {type: oauth2, flows: {implicit: {authorizationUrl: /a, scopes: {r: read}}}}
paths:
  /global: {get: {responses: {'200': {description: ok}}}}
  /optional: {get: {security: [{}, {key: []}], responses: {'200': {description: ok}}}}
  /both: {get: {security: [{key: [], basic: []}, {oauth: [r]}], responses: {'200': {description: ok}}}}
  /open: {get: {security: [], responses: {'200': {description: ok}}}}
`

func TestOperationSecurity(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	get := func(path string) usecase.OperationSecurity {
		sec, ok := e.OperationSecurity([]byte(securityDoc), "GET", path)
		if !ok {
			t.Fatalf("%s: operation not found", path)
		}

		return sec
	}

	if g := get("/global"); len(g.Alternatives) != 1 || g.Alternatives[0][0].Kind != usecase.SchemeBearer {
		t.Errorf("global security applies: %+v", g)
	}

	for _, path := range []string{"/optional", "/open"} {
		if s := get(path); len(s.Alternatives) != 0 {
			t.Errorf("%s must be public: %+v", path, s)
		}
	}

	both := get("/both")
	if len(both.Alternatives) != 2 || len(both.Alternatives[0]) != 2 {
		t.Fatalf("alternatives: %+v", both)
	}

	key, basic := both.Alternatives[0][1], both.Alternatives[0][0] // schemes are sorted by name.
	if basic.Kind != usecase.SchemeBasic || key.Kind != usecase.SchemeAPIKey || key.In != "header" || key.Param != "X-Key" {
		t.Errorf("scheme details: basic=%+v key=%+v", basic, key)
	}

	if oauth := both.Alternatives[1][0]; oauth.Kind != usecase.SchemeBearer || len(oauth.Scopes) != 1 || oauth.Scopes[0] != "r" {
		t.Errorf("oauth2 scopes: %+v", oauth)
	}

	if _, ok := e.OperationSecurity([]byte(securityDoc), "GET", "/missing"); ok {
		t.Error("unknown operation must not be found")
	}
}
