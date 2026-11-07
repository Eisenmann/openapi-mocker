package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/invopop/yaml"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// ErrUnknownExportTarget is returned for an export format that is not offered.
var ErrUnknownExportTarget = errors.New("unknown export format")

var _ usecase.ContractConverter = (*Engine)(nil)

// Normalize brings a Swagger 2.0 or OpenAPI 3.1 document to OpenAPI 3.0, the
// version the rest of the application works with. Other input (3.0 documents,
// GraphQL, MCP, anything that is not a JSON or YAML object) is returned
// unchanged with an empty note, so validation reports its own errors.
func (e *Engine) Normalize(raw string) (converted, note string, err error) {
	doc, ok := decodeObject(raw)
	if !ok {
		return raw, "", nil
	}

	switch {
	case doc["swagger"] == "2.0":
		out, err := swaggerToOpenAPI3(raw)
		if err != nil {
			return raw, "", err
		}

		return out, "converted from Swagger 2.0 to OpenAPI 3.0", nil
	case isOpenAPI31(doc):
		version := fmt.Sprint(doc["openapi"])
		dropped := downgradeTo30(doc)
		doc["openapi"] = "3.0.3"

		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return raw, "", fmt.Errorf("encode converted contract: %w", err)
		}

		note := "converted from OpenAPI " + version + " to OpenAPI 3.0"

		if len(dropped) > 0 {
			note += "; dropped unsupported keywords: " + strings.Join(dropped, ", ")
		}

		return string(out), note, nil
	default:
		return raw, "", nil
	}
}

// Export converts an OpenAPI 3.0 contract to the target format ("openapi3",
// "openapi31" or "swagger2") and encodes it as "json" or "yaml".
func (e *Engine) Export(raw, target, format string) (string, error) {
	var doc map[string]any

	switch target {
	case usecase.ExportOpenAPI3:
		obj, ok := decodeObject(raw)
		if !ok {
			return "", errNotObject
		}

		doc = obj
	case usecase.ExportOpenAPI31:
		obj, ok := decodeObject(raw)
		if !ok {
			return "", errNotObject
		}

		upgradeTo31(obj)
		doc = obj
	case usecase.ExportSwagger2:
		obj, err := openapi3ToSwagger(raw)
		if err != nil {
			return "", err
		}

		doc = obj
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownExportTarget, target)
	}

	return encodeDocument(doc, format)
}

var errNotObject = errors.New("the contract is not a JSON or YAML object")

func encodeDocument(doc map[string]any, format string) (string, error) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode contract: %w", err)
	}

	if format != "yaml" {
		return string(data) + "\n", nil
	}

	out, err := yaml.JSONToYAML(data)
	if err != nil {
		return "", fmt.Errorf("encode contract as YAML: %w", err)
	}

	return string(out), nil
}

// decodeObject parses a JSON or YAML document into a generic object.
func decodeObject(raw string) (map[string]any, bool) {
	data := []byte(strings.TrimSpace(raw))
	if len(data) == 0 {
		return nil, false
	}

	if data[0] != '{' {
		converted, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, false
		}

		data = converted
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, false
	}

	return doc, true
}

func isOpenAPI31(doc map[string]any) bool {
	v, ok := doc["openapi"].(string)

	return ok && strings.HasPrefix(v, "3.1")
}

func swaggerToOpenAPI3(raw string) (string, error) {
	data := []byte(raw)

	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		converted, err := yaml.YAMLToJSON(data)
		if err != nil {
			return "", fmt.Errorf("parse Swagger 2.0 contract: %w", err)
		}

		data = converted
	}

	var doc2 openapi2.T

	err := json.Unmarshal(data, &doc2)
	if err != nil {
		return "", fmt.Errorf("parse Swagger 2.0 contract: %w", err)
	}

	doc3, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return "", fmt.Errorf("convert Swagger 2.0 contract: %w", err)
	}

	out, err := json.MarshalIndent(doc3, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode converted contract: %w", err)
	}

	return string(out), nil
}

func openapi3ToSwagger(raw string) (map[string]any, error) {
	doc3, err := parse([]byte(raw))
	if err != nil {
		return nil, err
	}

	if doc3.Components == nil {
		doc3.Components = &openapi3.Components{}
	}

	doc2, err := openapi2conv.FromV3(doc3)
	if err != nil {
		return nil, fmt.Errorf("convert to Swagger 2.0: %w", err)
	}

	data, err := json.Marshal(doc2)
	if err != nil {
		return nil, fmt.Errorf("encode Swagger 2.0: %w", err)
	}

	obj, ok := decodeObject(string(data))
	if !ok {
		return nil, errNotObject
	}

	return obj, nil
}

// OpenAPI 3.1 to 3.0 conversion.

// unsupported30 are 3.1 / JSON Schema 2020-12 keywords with no 3.0 equivalent;
// they are removed.
var unsupported30 = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "$defs": true, "prefixItems": true,
	"patternProperties": true, "unevaluatedProperties": true, "unevaluatedItems": true,
	"if": true, "then": true, "else": true, "dependentSchemas": true, "dependentRequired": true,
	"contentMediaType": true, "contentEncoding": true, "propertyNames": true, "contains": true,
	"minContains": true, "maxContains": true,
}

// downgradeTo30 rewrites a 3.1 document into 3.0 in place and returns the
// names of the keywords it had to drop.
func downgradeTo30(doc map[string]any) []string {
	dropped := map[string]bool{}

	delete(doc, "webhooks")
	delete(doc, "jsonSchemaDialect")

	if info, ok := doc["info"].(map[string]any); ok {
		delete(info, "summary")

		if lic, ok := info["license"].(map[string]any); ok {
			delete(lic, "identifier")
		}
	}

	walk(doc, func(m map[string]any) { downgradeNode(m, dropped) })

	out := make([]string, 0, len(dropped))
	for k := range dropped {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func downgradeNode(m map[string]any, dropped map[string]bool) {
	if _, isRef := m["$ref"]; isRef {
		for k := range m { // 3.0 forbids siblings of $ref.
			if k != "$ref" {
				delete(m, k)
			}
		}

		return
	}

	for k := range m {
		if unsupported30[k] {
			delete(m, k)

			dropped[k] = true
		}
	}

	if v, ok := m["const"]; ok {
		delete(m, "const")
		m["enum"] = []any{v}
	}

	if list, ok := m["examples"].([]any); ok { // a schema's examples; media types use a map.
		delete(m, "examples")

		if len(list) > 0 {
			m["example"] = list[0]
		}
	}

	downgradeType(m)
	downgradeExclusive(m, "exclusiveMinimum", "minimum")
	downgradeExclusive(m, "exclusiveMaximum", "maximum")
}

// downgradeType turns type: [string, "null"] into type: string, nullable: true.
func downgradeType(m map[string]any) {
	list, ok := m["type"].([]any)
	if !ok {
		return
	}

	types := make([]any, 0, len(list))

	for _, t := range list {
		if t == "null" {
			m["nullable"] = true

			continue
		}

		types = append(types, t)
	}

	switch len(types) {
	case 0:
		delete(m, "type")
	case 1:
		m["type"] = types[0]
	default:
		delete(m, "type")

		anyOf := make([]any, 0, len(types))
		for _, t := range types {
			anyOf = append(anyOf, map[string]any{"type": t})
		}

		m["anyOf"] = anyOf
	}
}

// downgradeExclusive turns a numeric exclusiveMinimum (3.1) into minimum plus
// a boolean exclusiveMinimum (3.0).
func downgradeExclusive(m map[string]any, key, bound string) {
	n, ok := m[key].(json.Number)
	if !ok {
		return
	}

	m[bound] = n
	m[key] = true
}

// OpenAPI 3.0 to 3.1 conversion.

func upgradeTo31(doc map[string]any) {
	doc["openapi"] = "3.1.0"

	walk(doc, func(m map[string]any) {
		if nullable, ok := m["nullable"].(bool); ok {
			delete(m, "nullable")

			if nullable {
				addNullType(m)
			}
		}

		upgradeExclusive(m, "exclusiveMinimum", "minimum")
		upgradeExclusive(m, "exclusiveMaximum", "maximum")
	})
}

func addNullType(m map[string]any) {
	switch t := m["type"].(type) {
	case string:
		m["type"] = []any{t, "null"}
	case []any:
		m["type"] = append(t, "null")
	}
}

// upgradeExclusive turns a boolean exclusiveMinimum (3.0) into the numeric
// form of 3.1.
func upgradeExclusive(m map[string]any, key, bound string) {
	exclusive, ok := m[key].(bool)
	if !ok {
		return
	}

	delete(m, key)

	if exclusive {
		if v, ok := m[bound]; ok {
			m[key] = v

			delete(m, bound)
		}
	}
}

// walk calls fn for every object in the document, depth first.
func walk(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)

		for _, child := range t {
			walk(child, fn)
		}
	case []any:
		for _, child := range t {
			walk(child, fn)
		}
	}
}
