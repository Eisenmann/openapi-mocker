package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestPythonNativeAdapter_Supports(t *testing.T) {
	t.Parallel()

	a := NewPythonNativeAdapter()

	if !a.Supports(ports.LanguagePython) {
		t.Error("expected Python to be supported")
	}

	if a.Supports(ports.LanguageGo) {
		t.Error("expected Go to not be supported")
	}

	if a.Supports(ports.LanguageTypeScript) {
		t.Error("expected TypeScript to not be supported")
	}
}

func TestPythonNativeAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a := NewPythonNativeAdapter()

	if a.Strategy() != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", a.Strategy())
	}
}

func TestPythonNativeAdapter_Generate(t *testing.T) {
	t.Parallel()

	a := NewPythonNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguagePython,
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

	if result.Language != ports.LanguagePython {
		t.Errorf("expected Python language, got %s", result.Language)
	}

	if result.Strategy != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", result.Strategy)
	}

	// Server files: server_interface.py, router.py, unimplemented.py, server.py
	// Client files: client.py
	// requirements.txt, README.md
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
		"./out/server/server_interface.py",
		"./out/server/router.py",
		"./out/server/unimplemented.py",
		"./out/server/server.py",
		"./out/client/client.py",
		"./out/requirements.txt",
		"./out/README.md",
	}

	for _, p := range expectedPaths {
		if !paths[p] {
			t.Errorf("missing file: %s", p)
		}
	}

	// Verify server interface has operations.
	for _, f := range result.Files {
		if f.Path == "./out/server/server_interface.py" {
			content := string(f.Content)
			if !strings.Contains(content, "ServerInterface") {
				t.Error("server_interface.py missing ServerInterface")
			}
		}
	}

	// Verify client has operations.
	for _, f := range result.Files {
		if f.Path == "./out/client/client.py" {
			content := string(f.Content)
			if !strings.Contains(content, "class Client") {
				t.Error("client.py missing Client class")
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

func TestPythonNativeAdapter_Generate_InvalidSchema(t *testing.T) {
	t.Parallel()

	a := NewPythonNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguagePython,
		Schema:      &ports.OpenAPISchema{Content: []byte("invalid")},
		OutputPath:  "./out",
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

func TestPythonNativeAdapter_Generate_DefaultPackageName(t *testing.T) {
	t.Parallel()

	a := NewPythonNativeAdapter()

	req := &ports.GenerationRequest{
		Language:   ports.LanguagePython,
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

func TestToPyIdent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "GetUser", want: "getUser"},
		{name: "with underscore", in: "get_user", want: "get_user"},
		{name: "with dash", in: "get-user", want: "getUser"},
		{name: "empty", in: "", want: "operation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toPyIdent(tt.in)
			if got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
