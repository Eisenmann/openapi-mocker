package graphql_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
)

func TestEngine_OperationType(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	tests := []struct {
		name, query, op, want string
		wantErr               bool
	}{
		{"shorthand query", `{ ping }`, "", "query", false},
		{"query", `query Q { ping }`, "", "query", false},
		{"mutation", `mutation M { ping }`, "", "mutation", false},
		{"subscription", `subscription S { tick }`, "", "subscription", false},
		{"commented keyword", "# subscription\nquery Q { ping }", "", "query", false},
		{"one line, named", `query A { a } subscription B { b }`, "B", "subscription", false},
		{"two operations, no name", `query A { a } subscription B { b }`, "", "", true},
		{"unknown name", `query A { a }`, "Z", "", true},
		{"syntax error", `query {`, "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := e.OperationType(tc.query, tc.op)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("OperationType = %q, %v; want %q (error: %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
