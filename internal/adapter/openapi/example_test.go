package openapi_test

import (
	"encoding/json"
	"testing"

	openapi "github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
)

const composedContract = `openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: array
                items: {$ref: "#/components/schemas/Pet"}
  /pets/{id}:
    get:
      parameters:
        - {name: id, in: path, required: true, schema: {type: integer}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Owner"}
  /anything:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                anyOf:
                  - {type: object, properties: {kind: {type: string, example: first}}}
                  - {type: object, properties: {kind: {type: string, example: second}}}
components:
  schemas:
    NewPet:
      type: object
      properties:
        name: {type: string, example: Rex}
    Pet:
      allOf:
        - $ref: "#/components/schemas/NewPet"
        - type: object
          properties:
            id: {type: integer, example: 7}
    Owner:
      description: a person or a company, with a name of its own
      properties:
        label: {type: string, example: owner}
      oneOf:
        - type: object
          properties:
            person: {type: string, example: Ann}
        - type: object
          properties:
            company: {type: string, example: Acme}
`

func exampleJSON(t *testing.T, path string) any {
	t.Helper()

	body, _, err := openapi.NewEngine().ExampleResponse([]byte(composedContract), "GET", path, "200")
	if err != nil {
		t.Fatal(err)
	}

	var out any

	err = json.Unmarshal(body, &out)
	if err != nil {
		t.Fatalf("%s is not JSON: %s", path, body)
	}

	return out
}

func TestExampleResponse_AllOfMergesTheParts(t *testing.T) {
	t.Parallel()

	list, ok := exampleJSON(t, "/pets").([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("expected a one-item array, got %v", list)
	}

	pet, ok := list[0].(map[string]any)
	if !ok || pet["name"] != "Rex" || pet["id"] != float64(7) {
		t.Errorf("allOf must merge NewPet and the id, got %v", list[0])
	}
}

func TestExampleResponse_OneOfAndAnyOfUseTheFirstOption(t *testing.T) {
	t.Parallel()

	owner, ok := exampleJSON(t, "/pets/1").(map[string]any)
	if !ok || owner["person"] != "Ann" || owner["label"] != "owner" || owner["company"] != nil {
		t.Errorf("oneOf must add the first option to the schema's own properties, got %v", owner)
	}

	anything, ok := exampleJSON(t, "/anything").(map[string]any)
	if !ok || anything["kind"] != "first" {
		t.Errorf("anyOf must use the first option, got %v", anything)
	}
}
