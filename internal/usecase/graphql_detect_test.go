package usecase_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestIsGraphQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty", "", false},
		{"openapi json", `{"openapi": "3.0.0"}`, false},
		{"swagger json", `{"swagger": "2.0"}`, false},
		{"openapi yaml", "openapi: 3.0.0", false},
		{"swagger yaml", "swagger: 2.0", false},
		{"type definition", "type Query { ping: String }", true},
		{"schema definition", "schema { query: Query }", true},
		{"interface definition", "interface Node { id: ID! }", true},
		{"union definition", "union SearchResult = User | Admin", true},
		{"enum definition", "enum Color { RED GREEN }", true},
		{"input definition", "input CreateUserInput { name: String! }", true},
		{"scalar definition", "scalar DateTime", true},
		{"extend definition", "extend type Query { extra: String }", true},
		{"directive definition", "directive @auth on FIELD_DEFINITION", true},
		{"directive with @", "directive @deprecated on FIELD", true},
		{"multiline type", "type Query {\n  ping: String\n}", true},
		{"leading whitespace", "  type Query { ping: String }", true},
		{"graphql in description", "description: type Query { ping: String }", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := usecase.IsGraphQL(tt.raw)
			if got != tt.want {
				t.Errorf("IsGraphQL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
