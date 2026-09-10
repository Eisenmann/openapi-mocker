package codegen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

// mockExecutor is a fake CommandExecutor for testing.
type mockExecutor struct {
	output []byte
	err    error
	args   [][]string
}

func (m *mockExecutor) Execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.args = append(m.args, append([]string{name}, args...))
	return m.output, m.err
}

func TestOpenAPIGeneratorAdapter_Supports(t *testing.T) {
	t.Parallel()

	a := NewOpenAPIGeneratorAdapter(OpenAPIConfig{}, &mockExecutor{})

	if !a.Supports(ports.LanguageTypeScript) {
		t.Error("expected TypeScript to be supported")
	}
	if !a.Supports(ports.LanguagePython) {
		t.Error("expected Python to be supported")
	}
	if !a.Supports(ports.LanguageJava) {
		t.Error("expected Java to be supported")
	}
	if !a.Supports(ports.LanguageRust) {
		t.Error("expected Rust to be supported")
	}
	if !a.Supports(ports.LanguageCSharp) {
		t.Error("expected C# to be supported")
	}
	if !a.Supports(ports.LanguageGo) {
		t.Error("expected Go to be supported")
	}
	if a.Supports("unknown") {
		t.Error("expected unknown language to not be supported")
	}
}

func TestOpenAPIGeneratorAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a := NewOpenAPIGeneratorAdapter(OpenAPIConfig{}, &mockExecutor{})

	if a.Strategy() != ports.StrategyOpenAPI {
		t.Errorf("expected StrategyOpenAPI, got %s", a.Strategy())
	}
}

func TestOpenAPIGeneratorAdapter_Generate(t *testing.T) {
	t.Parallel()

	// Create a temp output directory
	outputDir := t.TempDir()

	// Create a generated file in the output dir to be collected
	genFile := filepath.Join(outputDir, "client.ts")
	if err := os.WriteFile(genFile, []byte("// generated client"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	executor := &mockExecutor{output: []byte("generation complete")}

	a := NewOpenAPIGeneratorAdapter(
		OpenAPIConfig{
			CLIPath: "openapi-generator-cli.jar",
			DefaultOptions: map[string]string{
				"enumPropertyNaming": "UPPERCASE",
			},
		},
		executor,
	)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageTypeScript,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI), Format: "yaml"},
		OutputPath:  outputDir,
		PackageName: "api",
		Metadata: ports.GenerationMetadata{
			ProjectName: "test",
			Version:     "1.0.0",
		},
		Options: map[string]interface{}{
			"additionalProp": "value",
		},
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Language != ports.LanguageTypeScript {
		t.Errorf("expected TypeScript, got %s", result.Language)
	}

	if result.Strategy != ports.StrategyOpenAPI {
		t.Errorf("expected StrategyOpenAPI, got %s", result.Strategy)
	}

	// Should have collected the generated file
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	if result.Files[0].Path != genFile {
		t.Errorf("expected path %s, got %s", genFile, result.Files[0].Path)
	}

	// Verify executor was called with java
	if len(executor.args) != 1 {
		t.Fatalf("expected 1 executor call, got %d", len(executor.args))
	}

	args := executor.args[0]
	if args[0] != "java" {
		t.Errorf("expected java, got %s", args[0])
	}

	// Verify args include -jar, generate, -g, -i, -o, -c
	found := map[string]bool{}
	for i, arg := range args {
		switch arg {
		case "-jar":
			found["-jar"] = true
			if i+1 < len(args) && args[i+1] != "openapi-generator-cli.jar" {
				t.Errorf("expected openapi-generator-cli.jar, got %s", args[i+1])
			}
		case "generate":
			found["generate"] = true
		case "-g":
			found["-g"] = true
			if i+1 < len(args) && args[i+1] != "typescript-axios" {
				t.Errorf("expected typescript-axios, got %s", args[i+1])
			}
		case "-i":
			found["-i"] = true
		case "-o":
			found["-o"] = true
			if i+1 < len(args) && args[i+1] != outputDir {
				t.Errorf("expected output dir %s, got %s", outputDir, args[i+1])
			}
		case "-c":
			found["-c"] = true
		}
	}

	for _, flag := range []string{"-jar", "generate", "-g", "-i", "-o", "-c"} {
		if !found[flag] {
			t.Errorf("missing flag %s in args", flag)
		}
	}
}

func TestOpenAPIGeneratorAdapter_Generate_UnsupportedLanguage(t *testing.T) {
	t.Parallel()

	a := NewOpenAPIGeneratorAdapter(OpenAPIConfig{}, &mockExecutor{})

	req := &ports.GenerationRequest{
		Language:   "unknown",
		Schema:     &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath: t.TempDir(),
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestOpenAPIGeneratorAdapter_Generate_ExecutorError(t *testing.T) {
	t.Parallel()

	executor := &mockExecutor{err: errors.New("execution failed")}

	a := NewOpenAPIGeneratorAdapter(OpenAPIConfig{CLIPath: "cli.jar"}, executor)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageTypeScript,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath:  t.TempDir(),
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from executor")
	}
}

func TestOpenAPIGeneratorAdapter_Generate_JSONSchema(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()

	executor := &mockExecutor{output: []byte("ok")}

	a := NewOpenAPIGeneratorAdapter(OpenAPIConfig{CLIPath: "cli.jar"}, executor)

	req := &ports.GenerationRequest{
		Language:    ports.LanguagePython,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI), Format: "json"},
		OutputPath:  outputDir,
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the schema file was written as .json
	args := executor.args[0]
	for i, arg := range args {
		if arg == "-i" && i+1 < len(args) {
			if filepath.Ext(args[i+1]) != ".json" {
				t.Errorf("expected .json schema file, got %s", args[i+1])
			}
		}
	}
}

func TestOpenAPIGeneratorAdapter_Generate_WithTemplateDir(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()

	executor := &mockExecutor{output: []byte("ok")}

	a := NewOpenAPIGeneratorAdapter(
		OpenAPIConfig{
			CLIPath:     "cli.jar",
			TemplateDir: "./templates",
		},
		executor,
	)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageJava,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath:  outputDir,
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify -t flag is present
	args := executor.args[0]
	found := false
	for i, arg := range args {
		if arg == "-t" && i+1 < len(args) && args[i+1] == "./templates" {
			found = true
		}
	}
	if !found {
		t.Error("expected -t ./templates in args")
	}
}

func TestOpenAPIGeneratorAdapter_Generate_CollectFilesError(t *testing.T) {
	t.Parallel()

	// Use a non-existent output dir to trigger filepath.Walk error
	executor := &mockExecutor{output: []byte("ok")}

	a := NewOpenAPIGeneratorAdapter(OpenAPIConfig{CLIPath: "cli.jar"}, executor)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageTypeScript,
		Schema:      &ports.OpenAPISchema{Content: []byte(testOpenAPI)},
		OutputPath:  filepath.Join(t.TempDir(), "nonexistent"),
		PackageName: "api",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for non-existent output dir")
	}
}

func TestDefaultGeneratorMap(t *testing.T) {
	t.Parallel()

	m := defaultGeneratorMap()

	if m[ports.LanguageTypeScript] != "typescript-axios" {
		t.Errorf("expected typescript-axios, got %s", m[ports.LanguageTypeScript])
	}
	if m[ports.LanguagePython] != "python" {
		t.Errorf("expected python, got %s", m[ports.LanguagePython])
	}
	if m[ports.LanguageJava] != "java" {
		t.Errorf("expected java, got %s", m[ports.LanguageJava])
	}
	if m[ports.LanguageRust] != "rust" {
		t.Errorf("expected rust, got %s", m[ports.LanguageRust])
	}
	if m[ports.LanguageCSharp] != "csharp" {
		t.Errorf("expected csharp, got %s", m[ports.LanguageCSharp])
	}
	if m[ports.LanguageGo] != "go" {
		t.Errorf("expected go, got %s", m[ports.LanguageGo])
	}
}
