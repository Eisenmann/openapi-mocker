package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestTypeScriptNativeAdapter_Supports(t *testing.T) {
	t.Parallel()

	a := NewTypeScriptNativeAdapter()

	if !a.Supports(ports.LanguageTypeScript) {
		t.Error("expected TypeScript to be supported")
	}

	if a.Supports(ports.LanguageGo) {
		t.Error("expected Go to not be supported")
	}

	if a.Supports(ports.LanguagePython) {
		t.Error("expected Python to not be supported")
	}
}

func TestTypeScriptNativeAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a := NewTypeScriptNativeAdapter()

	if a.Strategy() != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", a.Strategy())
	}
}

func TestTypeScriptNativeAdapter_Generate(t *testing.T) {
	t.Parallel()

	a := NewTypeScriptNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguageTypeScript,
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

	if result.Language != ports.LanguageTypeScript {
		t.Errorf("expected TypeScript language, got %s", result.Language)
	}

	if result.Strategy != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", result.Strategy)
	}

	// Server files: server_interface.ts, router.ts, unimplemented.ts, server.ts
	// Client files: client.ts
	// package.json, tsconfig.json
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
		"./out/server/server_interface.ts",
		"./out/server/router.ts",
		"./out/server/unimplemented.ts",
		"./out/server/server.ts",
		"./out/client/client.ts",
		"./out/package.json",
		"./out/tsconfig.json",
	}

	for _, p := range expectedPaths {
		if !paths[p] {
			t.Errorf("missing file: %s", p)
		}
	}

	// Verify package.json content.
	for _, f := range result.Files {
		if f.Path == "./out/package.json" {
			content := string(f.Content)
			if !strings.Contains(content, `"name": "api"`) {
				t.Error("package.json missing name")
			}
			if !strings.Contains(content, "typescript") {
				t.Error("package.json missing typescript dependency")
			}
		}
	}

	// Verify tsconfig.json content.
	for _, f := range result.Files {
		if f.Path == "./out/tsconfig.json" {
			content := string(f.Content)
			if !strings.Contains(content, "strict") {
				t.Error("tsconfig.json missing strict")
			}
		}
	}

	// Verify server interface has operations.
	for _, f := range result.Files {
		if f.Path == "./out/server/server_interface.ts" {
			content := string(f.Content)
			if !strings.Contains(content, "ServerInterface") {
				t.Error("server_interface.ts missing ServerInterface")
			}
		}
	}

	// Verify client has operations.
	for _, f := range result.Files {
		if f.Path == "./out/client/client.ts" {
			content := string(f.Content)
			if !strings.Contains(content, "class Client") {
				t.Error("client.ts missing Client class")
			}
		}
	}
}

func TestTypeScriptNativeAdapter_Generate_InvalidSchema(t *testing.T) {
	t.Parallel()

	a := NewTypeScriptNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguageTypeScript,
		Schema:      &ports.OpenAPISchema{Content: []byte("invalid")},
		OutputPath:  "./out",
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

func TestTypeScriptNativeAdapter_Generate_DefaultPackageName(t *testing.T) {
	t.Parallel()

	a := NewTypeScriptNativeAdapter()

	req := &ports.GenerationRequest{
		Language:   ports.LanguageTypeScript,
		Schema:     &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath: "./out",
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify default package name "api" is used.
	for _, f := range result.Files {
		if f.Path == "./out/package.json" {
			if !strings.Contains(string(f.Content), `"name": "api"`) {
				t.Error("expected default package name api")
			}
		}
	}
}

func TestToTSIdent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "GetUser", want: "GetUser"},
		{name: "with underscore", in: "get_user", want: "Get_user"},
		{name: "with dash", in: "get-user", want: "GetUser"},
		{name: "empty", in: "", want: "Operation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toTSIdent(tt.in)
			if got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
