package openapi_test

import (
	"strings"
	"testing"

	openapi "github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
)

const responseSpec = `openapi: 3.0.0
info:
  title: Pets
  version: 1.0.0
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - name: petId
          in: path
          required: true
          schema:
            type: integer
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                required: [id, name]
                properties:
                  id:
                    type: integer
                  name:
                    type: string
                  tags:
                    type: array
                    items:
                      type: object
                      properties:
                        label:
                          type: string
        '4XX':
          description: client error
          content:
            application/json:
              schema:
                type: object
        '204':
          description: gone
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
                  minLength: 3
                  maxLength: 8
      responses:
        default:
          description: anything
`

func newResponseValidator(t *testing.T, every int) *openapi.ResponseValidator {
	t.Helper()

	v, err := openapi.NewEngine().NewResponseValidator([]byte(responseSpec), every)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func TestResponseValidator_ValidResponse(t *testing.T) {
	t.Parallel()

	v := newResponseValidator(t, 1)

	viol, checked := v.ValidateResponse("GET", "/pets/7", 200, "application/json; charset=utf-8", []byte(`{"id":7,"name":"rex"}`))
	if !checked || len(viol) != 0 {
		t.Errorf("valid response: checked=%v violations=%v", checked, viol)
	}
}

func TestResponseValidator_BodyViolationsHavePaths(t *testing.T) {
	t.Parallel()

	v := newResponseValidator(t, 1)

	body := `{"id":"seven","tags":[{"label":3}]}`

	viol, checked := v.ValidateResponse("GET", "/pets/7", 200, "application/json", []byte(body))
	if !checked || len(viol) == 0 {
		t.Fatalf("expected violations, got checked=%v %v", checked, viol)
	}

	var paths []string
	for _, x := range viol {
		paths = append(paths, x.Path)
	}

	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "$.id") {
		t.Errorf("violation paths %q should include $.id", joined)
	}
}

func TestResponseValidator_StatusAndContentType(t *testing.T) {
	t.Parallel()

	v := newResponseValidator(t, 1)

	viol, _ := v.ValidateResponse("GET", "/pets/7", 500, "application/json", []byte(`{}`))
	if len(viol) != 1 || viol[0].Path != "status" {
		t.Errorf("undeclared status: %v", viol)
	}

	if viol, _ = v.ValidateResponse("GET", "/pets/7", 404, "application/json", []byte(`{}`)); len(viol) != 0 {
		t.Errorf("4XX class should match 404: %v", viol)
	}

	if viol, _ = v.ValidateResponse("GET", "/pets/7", 204, "", nil); len(viol) != 0 {
		t.Errorf("204 without body: %v", viol)
	}

	if viol, _ = v.ValidateResponse("GET", "/pets/7", 200, "text/html", []byte(`<p>`)); len(viol) == 0 {
		t.Error("unexpected Content-Type should be a violation")
	}

	if viol, _ = v.ValidateResponse("GET", "/pets/7", 200, "application/json", nil); len(viol) != 1 || viol[0].Path != "body" {
		t.Errorf("empty body: %v", viol)
	}
}

func TestResponseValidator_DefaultResponseAndUnknownOperation(t *testing.T) {
	t.Parallel()

	v := newResponseValidator(t, 1)

	if viol, checked := v.ValidateResponse("POST", "/pets", 201, "", nil); !checked || len(viol) != 0 {
		t.Errorf("default response: checked=%v %v", checked, viol)
	}

	if _, checked := v.ValidateResponse("GET", "/nowhere", 200, "", nil); checked {
		t.Error("unknown operation must be skipped")
	}
}

func TestResponseValidator_Sampling(t *testing.T) {
	t.Parallel()

	v := newResponseValidator(t, 4)

	checkedCount := 0

	for range 20 {
		if _, ok := v.ValidateResponse("GET", "/pets/7", 200, "application/json", []byte(`{"id":1,"name":"a"}`)); ok {
			checkedCount++
		}
	}

	if checkedCount != 5 {
		t.Errorf("sampled 1 in 4 of 20: checked %d, want 5", checkedCount)
	}
}

func TestNewResponseValidator_BadContract(t *testing.T) {
	t.Parallel()

	if _, err := openapi.NewEngine().NewResponseValidator([]byte("not: [a contract"), 1); err == nil {
		t.Error("expected an error for an unparsable contract")
	}
}
