package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestCSharpNativeAdapter_Supports(t *testing.T) {
	t.Parallel()

	a := NewCSharpNativeAdapter()

	if !a.Supports(ports.LanguageCSharp) {
		t.Error("expected C# to be supported")
	}

	if a.Supports(ports.LanguageGo) {
		t.Error("expected Go to not be supported")
	}

	if a.Supports(ports.LanguagePython) {
		t.Error("expected Python to not be supported")
	}
}

func TestCSharpNativeAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a := NewCSharpNativeAdapter()

	if a.Strategy() != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", a.Strategy())
	}
}

func TestCSharpNativeAdapter_Generate(t *testing.T) {
	t.Parallel()

	a := NewCSharpNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguageCSharp,
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

	if result.Language != ports.LanguageCSharp {
		t.Errorf("expected C# language, got %s", result.Language)
	}

	if result.Strategy != ports.StrategyNative {
		t.Errorf("expected StrategyNative, got %s", result.Strategy)
	}

	// Server files: ServerInterface.cs, Router.cs, UnimplementedServer.cs, Program.cs
	// Client files: Client.cs
	// .csproj, README.md
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
		"./out/server/ServerInterface.cs",
		"./out/server/Router.cs",
		"./out/server/UnimplementedServer.cs",
		"./out/server/Program.cs",
		"./out/client/Client.cs",
		"./out/api.csproj",
		"./out/README.md",
	}

	for _, p := range expectedPaths {
		if !paths[p] {
			t.Errorf("missing file: %s", p)
		}
	}

	// Verify server interface has operations.
	for _, f := range result.Files {
		if f.Path == "./out/server/ServerInterface.cs" {
			content := string(f.Content)
			if !strings.Contains(content, "ServerInterface") {
				t.Error("ServerInterface.cs missing ServerInterface")
			}
		}
	}

	// Verify client has operations.
	for _, f := range result.Files {
		if f.Path == "./out/client/Client.cs" {
			content := string(f.Content)
			if !strings.Contains(content, "class Client") {
				t.Error("Client.cs missing Client class")
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

func TestCSharpNativeAdapter_Generate_InvalidSchema(t *testing.T) {
	t.Parallel()

	a := NewCSharpNativeAdapter()

	req := &ports.GenerationRequest{
		Language:    ports.LanguageCSharp,
		Schema:      &ports.OpenAPISchema{Content: []byte("invalid")},
		OutputPath:  "./out",
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

func TestCSharpNativeAdapter_Generate_DefaultPackageName(t *testing.T) {
	t.Parallel()

	a := NewCSharpNativeAdapter()

	req := &ports.GenerationRequest{
		Language:   ports.LanguageCSharp,
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

func TestToCSIdent(t *testing.T) {
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

			got := toCSIdent(tt.in)
			if got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
