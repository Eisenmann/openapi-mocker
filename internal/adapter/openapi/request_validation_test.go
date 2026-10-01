package openapi_test

import (
	"strings"
	"testing"

	openapi "github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const requestSpec = `openapi: 3.0.0
info:
  title: Orders
  version: 1.0.0
paths:
  /orders:
    get:
      parameters:
        - name: limit
          in: query
          schema:
            type: integer
            minimum: 1
            maximum: 100
        - name: status
          in: query
          required: true
          schema:
            type: string
            enum: [open, closed]
        - name: X-Trace-Id
          in: header
          required: true
          schema:
            type: string
      responses:
        '200':
          description: OK
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name, quantity]
              properties:
                name:
                  type: string
                quantity:
                  type: integer
      responses:
        '201':
          description: Created
  /orders/{id}:
    get:
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: integer
      responses:
        '200':
          description: OK
`

func validate(t *testing.T, method, path string, query map[string][]string, header map[string][]string, body string) []string {
	t.Helper()

	return openapi.NewEngine().ValidateRequest([]byte(requestSpec), &usecase.RequestData{
		Method: method, Path: path, Query: query, Header: header, Body: []byte(body),
	})
}

func jsonHeader() map[string][]string {
	return map[string][]string{"Content-Type": {"application/json"}}
}

func assertViolation(t *testing.T, violations []string, want ...string) {
	t.Helper()

	joined := strings.Join(violations, " | ")
	if len(violations) == 0 {
		t.Fatalf("expected violations containing %q, got none", want)
	}

	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("violations %q do not mention %q", joined, w)
		}
	}
}

func TestValidateRequest_ValidRequests(t *testing.T) {
	t.Parallel()

	query := map[string][]string{"limit": {"10"}, "status": {"open"}}
	header := map[string][]string{"X-Trace-Id": {"abc"}}

	if v := validate(t, "GET", "/orders", query, header, ""); len(v) != 0 {
		t.Errorf("valid GET reported violations: %v", v)
	}

	if v := validate(t, "POST", "/orders", nil, jsonHeader(), `{"name":"a","quantity":2}`); len(v) != 0 {
		t.Errorf("valid POST reported violations: %v", v)
	}

	if v := validate(t, "GET", "/orders/42", nil, nil, ""); len(v) != 0 {
		t.Errorf("valid path param reported violations: %v", v)
	}
}

func TestValidateRequest_QueryParams(t *testing.T) {
	t.Parallel()

	header := map[string][]string{"X-Trace-Id": {"abc"}}

	assertViolation(t, validate(t, "GET", "/orders", map[string][]string{"limit": {"5"}}, header, ""),
		"query parameter", "status")
	assertViolation(t, validate(t, "GET", "/orders", map[string][]string{"status": {"open", "limit"}, "limit": {"abc"}}, header, ""),
		"limit")
	assertViolation(t, validate(t, "GET", "/orders", map[string][]string{"status": {"pending"}}, header, ""),
		"status")
	assertViolation(t, validate(t, "GET", "/orders", map[string][]string{"status": {"open"}, "limit": {"1000"}}, header, ""),
		"limit")
}

func TestValidateRequest_Headers(t *testing.T) {
	t.Parallel()

	assertViolation(t, validate(t, "GET", "/orders", map[string][]string{"status": {"open"}}, nil, ""),
		"header parameter", "X-Trace-Id")
}

func TestValidateRequest_PathParam(t *testing.T) {
	t.Parallel()

	assertViolation(t, validate(t, "GET", "/orders/abc", nil, nil, ""), "path parameter", "id")
}

func TestValidateRequest_Body(t *testing.T) {
	t.Parallel()

	assertViolation(t, validate(t, "POST", "/orders", nil, jsonHeader(), `{"name":"a"}`),
		"request body", "quantity")
	assertViolation(t, validate(t, "POST", "/orders", nil, jsonHeader(), `{"name":"a","quantity":"two"}`),
		"request body", "quantity")
	assertViolation(t, validate(t, "POST", "/orders", nil, jsonHeader(), ``),
		"request body")
	assertViolation(t, validate(t, "POST", "/orders", nil, jsonHeader(), `{not json`),
		"request body")
}

func TestValidateRequest_ContentTypeWithCharset(t *testing.T) {
	t.Parallel()

	header := map[string][]string{"Content-Type": {"application/json; charset=utf-8"}}
	if v := validate(t, "POST", "/orders", nil, header, `{"name":"a","quantity":2}`); len(v) != 0 {
		t.Errorf("charset parameter must be accepted, got %v", v)
	}
}

func TestValidateRequest_ContentType(t *testing.T) {
	t.Parallel()

	assertViolation(t, validate(t, "POST", "/orders", nil, map[string][]string{"Content-Type": {"text/plain"}}, `hi`),
		"request body")
}

func TestValidateRequest_ReportsAllViolations(t *testing.T) {
	t.Parallel()

	v := validate(t, "GET", "/orders", map[string][]string{"limit": {"x"}}, nil, "")
	if len(v) < 3 {
		t.Errorf("expected violations for limit, status and header, got %d: %v", len(v), v)
	}
}

func TestValidateRequest_NotApplicable(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	if v := validate(t, "GET", "/unknown", nil, nil, ""); v != nil {
		t.Errorf("unknown operation must not be a request violation, got %v", v)
	}

	v := e.ValidateRequest([]byte("not a contract"), &usecase.RequestData{Method: "GET", Path: "/orders"})
	if v != nil {
		t.Errorf("unparseable contract must not be a request violation, got %v", v)
	}
}
