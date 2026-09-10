package openapi_test

import (
	"strings"
	"testing"

	openapi "github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const testOpenAPI = `openapi: 3.0.0
info:
  title: Test API
  version: 1.0.0
paths:
  /users:
    get:
      summary: List users
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
                  properties:
                    id:
                      type: integer
                    name:
                      type: string
    post:
      summary: Create user
      responses:
        '201':
          description: Created
          content:
            application/json:
              schema:
                type: object
                properties:
                  id:
                    type: integer
                  name:
                    type: string
  /users/{id}:
    get:
      summary: Get user
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  id:
                    type: integer
                  name:
                    type: string
  /users/me:
    get:
      summary: Get current user
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  id:
                    type: integer
                  name:
                    type: string
`

func TestEngine_Validate(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	res := e.Validate([]byte(testOpenAPI))
	if !res.Valid {
		t.Fatalf("expected valid contract, got errors: %v", res.Errors)
	}

	if res.PathCount != 3 {
		t.Errorf("expected 3 paths, got %d", res.PathCount)
	}

	if res.OpCount != 4 {
		t.Errorf("expected 4 operations, got %d", res.OpCount)
	}

	bad := e.Validate([]byte("not: [valid"))
	if bad.Valid {
		t.Error("expected invalid contract to fail")
	}

	if len(bad.Errors) == 0 {
		t.Error("expected error messages")
	}
}

func TestEngine_ParseAndValidate(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	err := e.ParseAndValidate([]byte(testOpenAPI))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = e.ParseAndValidate([]byte("invalid"))
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}

func TestEngine_ListEndpoints(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	endpoints, err := e.ListEndpoints([]byte(testOpenAPI))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(endpoints) != 4 {
		t.Fatalf("expected 4 endpoints, got %d", len(endpoints))
	}

	// Sorted by path then method.
	if endpoints[0].Path != "/users" || endpoints[0].Method != "GET" {
		t.Errorf("unexpected first endpoint: %+v", endpoints[0])
	}

	if endpoints[1].Path != "/users" || endpoints[1].Method != "POST" {
		t.Errorf("unexpected second endpoint: %+v", endpoints[1])
	}

	if endpoints[2].Path != "/users/me" {
		t.Errorf("unexpected third endpoint: %+v", endpoints[2])
	}

	if endpoints[3].Path != "/users/{id}" {
		t.Errorf("unexpected fourth endpoint: %+v", endpoints[3])
	}

	// Invalid contract.
	_, err = e.ListEndpoints([]byte("invalid"))
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}

func TestEngine_FindOperation(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	// Exact match.
	tmpl, summary, found := e.FindOperation([]byte(testOpenAPI), "GET", "/users")
	if !found {
		t.Fatal("expected to find /users GET")
	}

	if tmpl != "/users" {
		t.Errorf("expected template /users, got %q", tmpl)
	}

	if summary != "List users" {
		t.Errorf("expected summary 'List users', got %q", summary)
	}

	// Template match.
	tmpl, _, found = e.FindOperation([]byte(testOpenAPI), "GET", "/users/42")
	if !found {
		t.Fatal("expected to find /users/42")
	}

	if tmpl != "/users/{id}" {
		t.Errorf("expected template /users/{id}, got %q", tmpl)
	}

	// Most specific match: /users/me beats /users/{id}.
	tmpl, _, found = e.FindOperation([]byte(testOpenAPI), "GET", "/users/me")
	if !found {
		t.Fatal("expected to find /users/me")
	}

	if tmpl != "/users/me" {
		t.Errorf("expected template /users/me, got %q", tmpl)
	}

	// Method not found.
	_, _, found = e.FindOperation([]byte(testOpenAPI), "DELETE", "/users")
	if found {
		t.Error("expected not found for DELETE /users")
	}

	// Path not found.
	_, _, found = e.FindOperation([]byte(testOpenAPI), "GET", "/nonexistent")
	if found {
		t.Error("expected not found for /nonexistent")
	}

	// Invalid contract.
	_, _, found = e.FindOperation([]byte("invalid"), "GET", "/users")
	if found {
		t.Error("expected not found for invalid contract")
	}
}

func TestEngine_ResponseSchemaJSON(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	schema, err := e.ResponseSchemaJSON([]byte(testOpenAPI), "GET", "/users", "200")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(schema, `"type": "array"`) {
		t.Errorf("expected array schema, got %q", schema)
	}

	// Operation not found.
	_, err = e.ResponseSchemaJSON([]byte(testOpenAPI), "GET", "/missing", "200")
	if err == nil {
		t.Fatal("expected error for missing operation")
	}

	// Invalid contract.
	_, err = e.ResponseSchemaJSON([]byte("invalid"), "GET", "/users", "200")
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}

func TestEngine_ExampleResponse(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	body, ct, err := e.ExampleResponse([]byte(testOpenAPI), "GET", "/users", "200")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	if len(body) == 0 {
		t.Error("expected non-empty body")
	}

	// Fallback to first available status code.
	body, _, err = e.ExampleResponse([]byte(testOpenAPI), "POST", "/users", "404")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(body) == 0 {
		t.Error("expected non-empty body for fallback status")
	}

	// Operation not found.
	_, _, err = e.ExampleResponse([]byte(testOpenAPI), "GET", "/missing", "200")
	if err == nil {
		t.Fatal("expected error for missing operation")
	}

	// Invalid contract.
	_, _, err = e.ExampleResponse([]byte("invalid"), "GET", "/users", "200")
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}

func TestEngine_ValidateResponseBody(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	// Valid body.
	err := e.ValidateResponseBody([]byte(testOpenAPI), "GET", "/users", "200", []byte(`[{"id": 1, "name": "Ada"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid body (wrong type).
	err = e.ValidateResponseBody([]byte(testOpenAPI), "GET", "/users", "200", []byte(`{"id": "not-int"}`))
	if err == nil {
		t.Fatal("expected error for invalid body")
	}

	// Operation not found.
	err = e.ValidateResponseBody([]byte(testOpenAPI), "GET", "/missing", "200", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for missing operation")
	}

	// Invalid contract.
	err = e.ValidateResponseBody([]byte("invalid"), "GET", "/users", "200", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}

func TestEngine_Diff(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	lines := e.Diff("a\nb\nc", "a\nx\nc")
	if len(lines) != 4 {
		t.Fatalf("expected 4 diff lines, got %d", len(lines))
	}

	// Identical.
	lines = e.Diff("a\nb", "a\nb")
	if len(lines) != 2 {
		t.Fatalf("expected 2 diff lines, got %d", len(lines))
	}

	for _, l := range lines {
		if l.Type != usecase.DiffSame {
			t.Errorf("expected all same, got %+v", l)
		}
	}

	// Empty.
	lines = e.Diff("", "")
	if len(lines) != 0 {
		t.Errorf("expected 0 diff lines for empty, got %d", len(lines))
	}
}
