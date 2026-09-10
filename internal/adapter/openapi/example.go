package openapi

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// exampleFromSchema recursively builds a plausible JSON value example from a
// JSON Schema. Used as a fallback when the endpoint has no manually configured
// or LLM-generated mock.
func exampleFromSchema(schemaRef *openapi3.SchemaRef, depth int) interface{} {
	if schemaRef == nil || schemaRef.Value == nil || depth > 8 {
		return nil
	}

	s := schemaRef.Value

	if s.Example != nil {
		return s.Example
	}

	if len(s.Enum) > 0 {
		return s.Enum[0]
	}

	switch {
	case len(s.Properties) > 0 || s.Type.Is("object"):
		obj := map[string]interface{}{}
		for name, propRef := range s.Properties {
			obj[name] = exampleFromSchema(propRef, depth+1)
		}

		if len(obj) == 0 && s.AdditionalProperties.Schema != nil {
			obj["key"] = exampleFromSchema(s.AdditionalProperties.Schema, depth+1)
		}

		return obj
	case s.Type.Is("array"):
		return []interface{}{exampleFromSchema(s.Items, depth+1)}
	case s.Type.Is("string"):
		return exampleString(s.Format)
	case s.Type.Is("integer"):
		return rand.Intn(1000)
	case s.Type.Is("number"):
		return roundTo2(rand.Float64() * 1000)
	case s.Type.Is("boolean"):
		return rand.Intn(2) == 0
	}

	return nil
}

func exampleString(format string) string {
	switch format {
	case "date-time":
		return time.Now().UTC().Format(time.RFC3339)
	case "date":
		return time.Now().UTC().Format("2006-01-02")
	case "email":
		return "user@example.com"
	case "uuid":
		return "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	case "uri", "url":
		return "https://example.com/resource"
	case "hostname":
		return "example.com"
	case "ipv4":
		return "192.0.2.1"
	default:
		return "string"
	}
}

func roundTo2(f float64) float64 { return float64(int(f*100)) / 100 }

// responseExample builds a JSON body example for a specific status code of an
// operation.
func responseExample(op *openapi3.Operation, statusCode string) (body []byte, contentType string, ok bool) {
	if op == nil || op.Responses == nil {
		return nil, "", false
	}

	respRef := op.Responses.Value(statusCode)
	if respRef == nil {
		respRef = op.Responses.Default()
	}

	if respRef == nil || respRef.Value == nil {
		return nil, "", false
	}

	mt := respRef.Value.Content.Get("application/json")
	if mt == nil {
		return []byte(""), "", true
	}

	if mt.Example != nil {
		b, _ := json.MarshalIndent(mt.Example, "", "  ")
		return b, "application/json", true
	}

	if len(mt.Examples) > 0 {
		for _, ex := range mt.Examples {
			if ex.Value != nil {
				b, _ := json.MarshalIndent(ex.Value.Value, "", "  ")
				return b, "application/json", true
			}
		}
	}

	val := exampleFromSchema(mt.Schema, 0)
	b, _ := json.MarshalIndent(val, "", "  ")

	return b, "application/json", true
}

// responseSchemaJSON returns the JSON Schema of the response for the given
// status code of an operation (used when generating mock data via LLM).
func responseSchemaJSON(op *openapi3.Operation, statusCode string) (string, error) {
	if op == nil || op.Responses == nil {
		return "{}", nil
	}

	respRef := op.Responses.Value(statusCode)
	if respRef == nil {
		respRef = op.Responses.Default()
	}

	if respRef == nil || respRef.Value == nil {
		return "{}", nil
	}

	mt := respRef.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil {
		return "{}", nil
	}

	b, err := json.MarshalIndent(mt.Schema.Value, "", "  ")
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// validateBodyAgainstResponseSchema checks that the body matches the JSON
// Schema declared in the contract for the given status code of an operation.
func validateBodyAgainstResponseSchema(op *openapi3.Operation, statusCode string, body []byte) error {
	if op == nil || op.Responses == nil || len(body) == 0 {
		return nil
	}

	respRef := op.Responses.Value(statusCode)
	if respRef == nil {
		respRef = op.Responses.Default()
	}

	if respRef == nil || respRef.Value == nil {
		return nil // no description for this status code in the contract — nothing to validate against.
	}

	mt := respRef.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		return nil
	}

	var data interface{}
	err := json.Unmarshal(body, &data)
	if err != nil {
		return fmt.Errorf("body is not valid JSON: %w", err)
	}

	err = mt.Schema.Value.VisitJSON(data)
	if err != nil {
		return fmt.Errorf("body does not match response schema: %w", err)
	}

	return nil
}
