package usecase_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestIsMCP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty", "", false},
		{"openapi json", `{"openapi": "3.0.0"}`, false},
		{"openapi yaml", "openapi: 3.0.0", false},
		{"graphql sdl", "type Query { ping: String }", false},
		{"canonical mcpServer root", `{"mcpServer": {"name": "x", "tools": []}}`, true},
		{"empty mcpServer root", `{"mcpServer": {}}`, true},
		{"tools plus serverInfo", `{"serverInfo": {"name": "x"}, "tools": []}`, true},
		{"tools alone", `{"tools": []}`, false},
		{"serverInfo alone", `{"serverInfo": {"name": "x"}}`, false},
		{"malformed json", `{"mcpServer": `, false},
		{"leading whitespace", `  {"mcpServer": {}}`, true},
		{"nulls", `{"mcpServer": null, "tools": null}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := usecase.IsMCP(tt.raw)
			if got != tt.want {
				t.Errorf("IsMCP(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
