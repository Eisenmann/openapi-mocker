package openapi_test

import (
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const baseSpec = `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /users/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: integer}}
    get:
      parameters:
        - {name: limit, in: query, schema: {type: integer}}
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                required: [id, name]
                properties:
                  id: {type: integer}
                  name: {type: string}
                  status: {type: string, enum: [active, blocked]}
                  tags: {type: array, items: {type: object, properties: {label: {type: string}}}}
        '404': {description: Not found}
    put:
      requestBody:
        required: false
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
                role: {type: string, enum: [admin, user]}
                nick: {type: string}
      responses:
        '200': {description: OK}
  /ping:
    get:
      responses:
        '200': {description: OK}
`

func changesOf(t *testing.T, oldSpec, newSpec string) map[string]domain.ContractChange {
	t.Helper()

	got, err := openapi.NewEngine().BreakingChanges(oldSpec, newSpec)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]domain.ContractChange{}
	for _, c := range got {
		out[c.Kind+"|"+c.Operation+"|"+c.Location] = c
	}

	return out
}

func TestBreakingChanges_NoChange(t *testing.T) {
	t.Parallel()

	if got := changesOf(t, baseSpec, baseSpec); len(got) != 0 {
		t.Errorf("identical contracts must not differ: %v", got)
	}
}

func TestBreakingChanges_Detected(t *testing.T) {
	t.Parallel()

	repl := strings.NewReplacer(
		"  /ping:\n    get:\n      responses:\n        '200': {description: OK}\n", "", // operation removed.
		// name removed and id retyped in one go.
		"                  id: {type: integer}\n                  name: {type: string}\n", "                  id: {type: string}\n",
		"enum: [active, blocked]", "enum: [active, blocked, deleted]", // response enum widened.
		"        - {name: limit, in: query, schema: {type: integer}}", "        - {name: limit, in: query, required: true, schema: {type: integer}}\n        - {name: token, in: header, required: true, schema: {type: string}}",
		"        required: false\n", "        required: true\n",
		"required: [name]\n              properties:\n                name: {type: string}\n                role: {type: string, enum: [admin, user]}\n                nick: {type: string}",
		"required: [name, email]\n              properties:\n                name: {type: string}\n                email: {type: string}\n                role: {type: string, enum: [admin]}",
	)
	newSpec := repl.Replace(baseSpec)

	// "name" is required in the response and also removed there, which is one change.
	got := changesOf(t, baseSpec, newSpec)

	want := []struct {
		key string
		sev domain.ChangeSeverity
	}{
		{"operation-removed|GET /ping|", domain.SeverityBreaking},
		{"field-removed|GET /users/{id}|response 200 application/json: name", domain.SeverityBreaking},
		{"type-changed|GET /users/{id}|response 200 application/json: id", domain.SeverityBreaking},
		{"enum-value-added|GET /users/{id}|response 200 application/json: status", domain.SeverityWarning},
		{"param-became-required|GET /users/{id}|query parameter limit", domain.SeverityBreaking},
		{"param-added-required|GET /users/{id}|header parameter token", domain.SeverityBreaking},
		{"request-body-required|PUT /users/{id}|request body", domain.SeverityBreaking},
		{"field-added-required|PUT /users/{id}|request body application/json: email", domain.SeverityBreaking},
		{"field-removed|PUT /users/{id}|request body application/json: nick", domain.SeverityWarning},
		{"enum-value-removed|PUT /users/{id}|request body application/json: role", domain.SeverityBreaking},
	}

	for _, w := range want {
		c, ok := got[w.key]
		if !ok {
			t.Errorf("missing change %s\n got: %v", w.key, keys(got))

			continue
		}

		if c.Severity != w.sev {
			t.Errorf("%s: severity %s, want %s", w.key, c.Severity, w.sev)
		}
	}
}

func TestBreakingChanges_CompatibleChangesAreSilent(t *testing.T) {
	t.Parallel()

	newSpec := strings.NewReplacer(
		"  /ping:", "  /pong:\n    get:\n      responses:\n        '200': {description: OK}\n  /ping:",
		"                  tags:", "                  extra: {type: string}\n                  tags:",
		"                nick: {type: string}", "                nick: {type: string}\n                optional: {type: string}",
		"enum: [admin, user]", "enum: [admin, user, guest]",
	).Replace(baseSpec)

	if got := changesOf(t, baseSpec, newSpec); len(got) != 0 {
		t.Errorf("additions must not be reported: %v", keys(got))
	}
}

func TestBreakingChanges_ResponseRemovedAndNestedAndOptional(t *testing.T) {
	t.Parallel()

	newSpec := strings.NewReplacer(
		"        '404': {description: Not found}\n", "",
		"required: [id, name]", "required: [id]",
		"properties: {label: {type: string}}", "properties: {label: {type: integer}}",
	).Replace(baseSpec)

	got := changesOf(t, baseSpec, newSpec)

	for _, key := range []string{
		"response-removed|GET /users/{id}|response 404",
		"field-became-optional|GET /users/{id}|response 200 application/json: name",
		"type-changed|GET /users/{id}|response 200 application/json: tags[].label",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %s\n got: %v", key, keys(got))
		}
	}

	if got["response-removed|GET /users/{id}|response 404"].Severity != domain.SeverityWarning {
		t.Error("removing an error response is only a warning")
	}
}

func TestBreakingChanges_InvalidContract(t *testing.T) {
	t.Parallel()

	if _, err := openapi.NewEngine().BreakingChanges(baseSpec, "not: [valid"); err == nil {
		t.Error("an unreadable contract must be an error")
	}
}

func keys(m map[string]domain.ContractChange) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
