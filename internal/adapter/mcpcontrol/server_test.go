package mcpcontrol

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func testServer() *Server {
	return New(&Deps{LoadRuns: &usecase.LoadRunService{}})
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()

	var m map[string]any

	err := json.Unmarshal(b, &m)
	if err != nil {
		t.Fatalf("not JSON: %v: %s", err, b)
	}

	return m
}

// TestToolSchemasMatchArguments keeps every schema and its argument type in step:
// a property without a field would be rejected, a field without a property
// would be invisible to the agent.
func TestToolSchemasMatchArguments(t *testing.T) {
	t.Parallel()

	for _, tl := range testServer().tools {
		properties, _ := tl.schema["properties"].(props)
		fields := map[string]bool{}

		for i := range tl.args.NumField() {
			tag, _, _ := strings.Cut(tl.args.Field(i).Tag.Get("json"), ",")
			fields[tag] = true
		}

		for name := range properties {
			if !fields[name] {
				t.Errorf("%s: schema property %q has no field", tl.name, name)
			}
		}

		for name := range fields {
			if _, ok := properties[name]; !ok {
				t.Errorf("%s: field %q is not in the schema", tl.name, name)
			}
		}

		if required, ok := tl.schema["required"].([]string); ok {
			for _, name := range required {
				if _, found := properties[name]; !found {
					t.Errorf("%s: required %q is not a property", tl.name, name)
				}
			}
		}

		if tl.description == "" || tl.title == "" || tl.args.Kind() != reflect.Struct {
			t.Errorf("%s: incomplete definition", tl.name)
		}
	}
}

func TestToolNamesAreUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for _, n := range testServer().ToolNames() {
		if seen[n] {
			t.Errorf("duplicate tool %s", n)
		}

		seen[n] = true
	}
}

func TestHandle_Protocol(t *testing.T) {
	t.Parallel()

	s := testServer()
	ctx := context.Background()

	init := decode(t, s.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize",`+
		`"params":{"protocolVersion":"2024-11-05"}}`)))
	if init["result"].(map[string]any)["protocolVersion"] != "2024-11-05" {
		t.Errorf("should echo a supported version: %v", init)
	}

	if s.Handle(ctx, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) != nil {
		t.Error("a notification gets no reply")
	}

	if decode(t, s.Handle(ctx, []byte(`not json`)))["error"].(map[string]any)["code"] != float64(codeParse) {
		t.Error("expected a parse error")
	}

	unknown := decode(t, s.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"nope"}`)))
	if unknown["error"].(map[string]any)["code"] != float64(codeMethodNotFound) {
		t.Errorf("expected method not found: %v", unknown)
	}

	missing := decode(t, s.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call",`+
		`"params":{"name":"nope"}}`)))
	if missing["error"].(map[string]any)["code"] != float64(codeInvalidParams) {
		t.Errorf("expected invalid params for an unknown tool: %v", missing)
	}
}
