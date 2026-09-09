package usecase_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// TestIsGraphQL covers the content-sniffing heuristic that distinguishes
// GraphQL SDL from OpenAPI YAML/JSON. The heuristic must be conservative:
// an OpenAPI document containing a GraphQL snippet inside a description
// must NOT be misdetected as GraphQL, and extension-only/directive-only SDL
// must be recognized as GraphQL.
func TestIsGraphQL(t *testing.T) {
	t.Parallel()

	for _, tt := range isGraphQLPositiveCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := usecase.IsGraphQL(tt.raw); got != tt.want {
				t.Errorf("IsGraphQL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}

	for _, tt := range isGraphQLNegativeCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := usecase.IsGraphQL(tt.raw); got != tt.want {
				t.Errorf("IsGraphQL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

type isGraphQLCase struct {
	name string
	raw  string
	want bool
}

func isGraphQLPositiveCases() []isGraphQLCase {
	return []isGraphQLCase{
		{
			name: "simple type definition",
			raw:  "type Query { ping: String! }",
			want: true,
		},
		{
			name: "schema block",
			raw:  "schema { query: Query }\ntype Query { ping: String! }",
			want: true,
		},
		{
			name: "interface",
			raw:  "interface Node { id: ID! }",
			want: true,
		},
		{
			name: "union",
			raw:  "union SearchResult = User | Admin",
			want: true,
		},
		{
			name: "enum",
			raw:  "enum Color { RED GREEN }",
			want: true,
		},
		{
			name: "input",
			raw:  "input CreateUserInput { name: String! }",
			want: true,
		},
		{
			name: "scalar",
			raw:  "scalar DateTime",
			want: true,
		},
		{
			name: "extension-only SDL",
			raw:  "extend type Query { newField: String }",
			want: true,
		},
		{
			name: "directive-only SDL",
			raw:  "directive @auth(role: String) on FIELD_DEFINITION",
			want: true,
		},
	}
}

func isGraphQLNegativeCases() []isGraphQLCase {
	return []isGraphQLCase{
		{
			name: "openapi yaml",
			raw:  "openapi: 3.0.3\ninfo:\n  title: Test\npaths: {}\n",
			want: false,
		},
		{
			name: "openapi json",
			raw:  `{"openapi":"3.0.3","info":{"title":"Test"},"paths":{}}`,
			want: false,
		},
		{
			name: "swagger yaml",
			raw:  "swagger: '2.0'\ninfo:\n  title: Test\npaths: {}\n",
			want: false,
		},
		{
			name: "openapi yaml with graphql snippet in description",
			raw: `openapi: 3.0.3
info:
  title: Test
  description: |
    Example GraphQL schema:
    type Query {
      ping: String!
    }
paths: {}
`,
			want: false,
		},
		{
			name: "empty",
			raw:  "",
			want: false,
		},
		{
			name: "whitespace only",
			raw:  "   \n  \n",
			want: false,
		},
		{
			name: "yaml with type key",
			raw:  "type: object\nproperties:\n  name:\n    type: string\n",
			want: false,
		},
	}
}
