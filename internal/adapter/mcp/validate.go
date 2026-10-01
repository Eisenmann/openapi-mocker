package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// callState carries per-request options and validation results through the
// JSON-RPC dispatch: the mock scenario, whether tools/call arguments are
// validated, whether violations reject the call, and the violations found.
type callState struct {
	scenario   string
	validate   bool
	enforce    bool
	violations []string
}

// check validates the arguments of a tools/call against the tool's
// inputSchema when validation is on, records the violations in st and
// returns the ones found for this call.
func (st *callState) check(tool *manifestTool, name string, args json.RawMessage) []string {
	if !st.validate {
		return nil
	}

	violations := validateArguments(tool.InputSchema, args)
	for _, v := range violations {
		st.violations = append(st.violations, "tools/call "+name+": "+v)
	}

	return violations
}

// validateArguments checks tools/call arguments against a tool's JSON Schema.
// Missing arguments are treated as an empty object so required properties are
// reported. A schema that cannot be loaded or evaluated is not a client
// error, so it yields no violations.
func validateArguments(inputSchema, args json.RawMessage) []string {
	if len(inputSchema) == 0 {
		return nil
	}

	var schema openapi3.Schema

	err := json.Unmarshal(inputSchema, &schema)
	if err != nil {
		return nil
	}

	var value any = map[string]any{}

	if len(args) > 0 && string(args) != "null" {
		err = json.Unmarshal(args, &value)
		if err != nil {
			return []string{"arguments: not valid JSON"}
		}
	}

	return schemaViolations(schema.VisitJSON(value, openapi3.MultiErrors()))
}

// schemaViolations flattens a (possibly multi-)error from schema validation
// into one message per violation, e.g. `arguments.city: property "city" is
// missing`. Errors that are not schema violations are ignored.
func schemaViolations(err error) []string {
	if err == nil {
		return nil
	}

	var multi openapi3.MultiError
	if errors.As(err, &multi) {
		out := make([]string, 0, len(multi))

		for _, e := range multi {
			out = append(out, schemaViolations(e)...)
		}

		return out
	}

	var se *openapi3.SchemaError
	if !errors.As(err, &se) {
		return nil
	}

	where := "arguments"
	if p := se.JSONPointer(); len(p) > 0 {
		where += "." + strings.Join(p, ".")
	}

	reason := se.Reason
	if reason == "" {
		reason, _, _ = strings.Cut(se.Error(), "\n")
	}

	return []string{fmt.Sprintf("%s: %s", where, reason)}
}

// warningsMetaKey is the result "_meta" entry that carries the validation
// warnings of a served (warn mode) tools/call, so MCP clients can see them.
const warningsMetaKey = "openapi-mocker/validationWarnings"

// withValidationWarnings attaches violations to a tools/call result as
// _meta["openapi-mocker/validationWarnings"] (MCP results may carry _meta).
// Existing _meta entries of the mock are kept. The result is returned
// unchanged when there are no violations.
func withValidationWarnings(result map[string]any, violations []string) map[string]any {
	if len(violations) == 0 {
		return result
	}

	meta, ok := result["_meta"].(map[string]any)
	if !ok {
		meta = map[string]any{}
	}

	meta[warningsMetaKey] = violations
	result["_meta"] = meta

	return result
}
