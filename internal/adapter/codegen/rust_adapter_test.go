package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestRustNativeAdapter_Supports(t *testing.T) {
	t.Parallel()

	a := NewRustNativeAdapter()

	if !a.Supports(ports.LanguageRust) {
		t.Error("expected Rust to be supported")
	}

	if a.Supports(ports.LanguageGo) {
		t.Error("expected Go to not be supported")
	}

	if a.Supports(ports.LanguagePython) {
		t.Error("expected Python to not be supported")
	}
}

func TestRustNativeAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a := NewRustNativeAdapter()

	if a.Strategy() != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", a.Strategy())
	}
}

func TestRustNativeAdapter_Generate(t *testing.T) {
	t.Parallel()

	a := NewRustNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguageRust,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath:  "./out",
		PackageName: "api",
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Language != ports.LanguageRust {
		t.Errorf("expected Rust language, got %s", result.Language)
	}

	if result.Strategy != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", result.Strategy)
	}

	// Server files: server_interface.rs, router.rs, unimplemented.rs, main.rs
	// Client files: client.rs
	// Cargo.toml, README.md
	// Total: 7
	if len(result.Files) != 7 {
		t.Fatalf("expected 7 files, got %d", len(result.Files))
	}

	// Verify file paths.
	paths := map[string]bool{}
	for _, f := range result.Files {
		paths[f.Path] = true
	}

	expectedPaths := []string{
		"./out/server/server_interface.rs",
		"./out/server/router.rs",
		"./out/server/unimplemented.rs",
		"./out/server/main.rs",
		"./out/client/client.rs",
		"./out/Cargo.toml",
		"./out/README.md",
	}

	for _, p := range expectedPaths {
		if !paths[p] {
			t.Errorf("missing file: %s", p)
		}
	}

	// Verify server interface has operations.
	for _, f := range result.Files {
		if f.Path == "./out/server/server_interface.rs" {
			content := string(f.Content)
			if !strings.Contains(content, "ServerInterface") {
				t.Error("server_interface.rs missing ServerInterface")
			}
		}
	}

	// Verify client has operations.
	for _, f := range result.Files {
		if f.Path == "./out/client/client.rs" {
			content := string(f.Content)
			if !strings.Contains(content, "pub struct Client") {
				t.Error("client.rs missing Client struct")
			}
		}
	}

	// Verify README has package name.
	for _, f := range result.Files {
		if f.Path == "./out/README.md" {
			content := string(f.Content)
			if !strings.Contains(content, "# api") {
				t.Error("README.md missing package name")
			}
		}
	}
}

func TestRustNativeAdapter_Generate_InvalidSchema(t *testing.T) {
	t.Parallel()

	a := NewRustNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguageRust,
		Schema:      &ports.OpenAPISchema{Content: []byte("invalid")},
		OutputPath:  "./out",
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

func TestRustNativeAdapter_Generate_DefaultPackageName(t *testing.T) {
	t.Parallel()

	a := NewRustNativeAdapter()

	req := &ports.GenerationRequest{
		Language:   ports.LanguageRust,
		Schema:     &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath: "./out",
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify default package name "api" is used.
	for _, f := range result.Files {
		if f.Path == "./out/README.md" {
			if !strings.Contains(string(f.Content), "# api") {
				t.Error("expected default package name api")
			}
		}
	}
}

func TestToRustIdent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "GetUser", want: "get_user"},
		{name: "with underscore", in: "get_user", want: "get_user"},
		{name: "with dash", in: "get-user", want: "get_user"},
		{name: "empty", in: "", want: "operation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toRustIdent(tt.in)
			if got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
