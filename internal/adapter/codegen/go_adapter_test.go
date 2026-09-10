package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestGoNativeAdapter_Supports(t *testing.T) {
	t.Parallel()

	a, err := NewGoNativeAdapter(GoConfig{ModulePath: "test", GoVersion: "1.21"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !a.Supports(ports.LanguageGo) {
		t.Error("expected Go to be supported")
	}

	if a.Supports(ports.LanguageTypeScript) {
		t.Error("expected TypeScript to not be supported")
	}

	if a.Supports(ports.LanguagePython) {
		t.Error("expected Python to not be supported")
	}
}

func TestGoNativeAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a, err := NewGoNativeAdapter(GoConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if a.Strategy() != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", a.Strategy())
	}
}

func TestGoNativeAdapter_Generate(t *testing.T) {
	t.Parallel()

	a, err := NewGoNativeAdapter(GoConfig{ModulePath: "example.com/test", GoVersion: "1.21"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := &ports.GenerationRequest{
		Language:    ports.LanguageGo,
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

	if result.Language != ports.LanguageGo {
		t.Errorf("expected Go language, got %s", result.Language)
	}

	if result.Strategy != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", result.Strategy)
	}

	// Server files: server_interface.go, router.go, unimplemented.go, cmd_main_example.go
	// Client files: client.go
	// go.mod: 1
	// Total: 6
	if len(result.Files) != 6 {
		t.Fatalf("expected 6 files, got %d", len(result.Files))
	}

	// Verify file paths
	paths := map[string]bool{}
	for _, f := range result.Files {
		paths[f.Path] = true
	}

	expectedPaths := []string{
		"./out/server/server_interface.go",
		"./out/server/router.go",
		"./out/server/unimplemented.go",
		"./out/server/cmd_main_example.go",
		"./out/client/client.go",
		"./out/go.mod",
	}

	for _, p := range expectedPaths {
		if !paths[p] {
			t.Errorf("missing file: %s", p)
		}
	}

	// Verify go.mod content
	for _, f := range result.Files {
		if f.Path == "./out/go.mod" {
			content := string(f.Content)
			if !strings.Contains(content, "module example.com/test") {
				t.Error("go.mod missing module path")
			}
			if !strings.Contains(content, "go 1.21") {
				t.Error("go.mod missing go version")
			}
		}
	}
}

func TestGoNativeAdapter_Generate_InvalidSchema(t *testing.T) {
	t.Parallel()

	a, err := NewGoNativeAdapter(GoConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := &ports.GenerationRequest{
		Language:    ports.LanguageGo,
		Schema:      &ports.OpenAPISchema{Content: []byte("invalid")},
		OutputPath:  "./out",
		PackageName: "api",
	}

	_, err = a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

func TestGoNativeAdapter_Generate_DefaultPackageName(t *testing.T) {
	t.Parallel()

	a, err := NewGoNativeAdapter(GoConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := &ports.GenerationRequest{
		Language:   ports.LanguageGo,
		Schema:     &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath: "./out",
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify default package name "api" is used
	for _, f := range result.Files {
		if strings.Contains(f.Path, "server_interface.go") {
			if !strings.Contains(string(f.Content), "package api") {
				t.Error("expected default package name api")
			}
		}
	}
}

func TestGoNativeAdapter_Generate_DefaultModulePath(t *testing.T) {
	t.Parallel()

	a, err := NewGoNativeAdapter(GoConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := &ports.GenerationRequest{
		Language:    ports.LanguageGo,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath:  "./out",
		PackageName: "api",
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, f := range result.Files {
		if f.Path == "./out/go.mod" {
			if !strings.Contains(string(f.Content), "module generated") {
				t.Error("expected default module path generated")
			}
		}
	}
}

func TestFormatGo(t *testing.T) {
	t.Parallel()

	// Valid Go source
	src := []byte("package main\nfunc main() {}\n")
	formatted := formatGo(src)
	if !strings.Contains(string(formatted), "package main") {
		t.Error("expected formatted output to contain package main")
	}

	// Invalid Go source - should return raw bytes
	invalid := []byte("not valid go")
	got := formatGo(invalid)
	if string(got) != string(invalid) {
		t.Error("expected raw bytes for invalid Go source")
	}
}

func TestExecuteTemplate(t *testing.T) {
	t.Parallel()

	// Valid template
	data := map[string]string{"Name": "World"}
	out, err := executeTemplate("test", "Hello {{.Name}}", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(out))
	}

	// Invalid template
	_, err = executeTemplate("bad", "{{.Invalid", data)
	if err == nil {
		t.Fatal("expected error for invalid template")
	}
}
