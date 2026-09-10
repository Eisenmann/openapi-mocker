package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// ---------- Mock adapters ----------

type mockCodeGenAdapter struct {
	langs     []ports.Language
	strategy  ports.GenerationStrategy
	result    *ports.GenerationResult
	err       error
	generated []*ports.GenerationRequest
}

func (m *mockCodeGenAdapter) Generate(ctx context.Context, req *ports.GenerationRequest) (*ports.GenerationResult, error) {
	m.generated = append(m.generated, req)
	return m.result, m.err
}

func (m *mockCodeGenAdapter) Supports(lang ports.Language) bool {
	for _, l := range m.langs {
		if l == lang {
			return true
		}
	}
	return false
}

func (m *mockCodeGenAdapter) Strategy() ports.GenerationStrategy {
	return m.strategy
}

// ---------- Mock router ----------

type mockRouter struct {
	strategy ports.GenerationStrategy
}

func (m *mockRouter) SelectStrategy(language ports.Language, req *ports.GenerationRequest) ports.GenerationStrategy {
	return m.strategy
}

// ---------- Mock logger ----------

type mockLogger struct{}

func (m *mockLogger) Info(msg string, keyvals ...interface{})  {}
func (m *mockLogger) Error(msg string, keyvals ...interface{}) {}

// ---------- Tests ----------

func TestCodeGeneratorUseCase_Generate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schema := &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")}

	goAdapter := &mockCodeGenAdapter{
		langs:    []ports.Language{ports.LanguageGo},
		strategy: ports.StrategyNative,
		result: &ports.GenerationResult{
			Files:    []ports.GeneratedFile{{Path: "server.go", Content: []byte("package main")}},
			Language: ports.LanguageGo,
			Strategy: ports.StrategyNative,
		},
	}

	uc := usecase.NewCodeGeneratorUseCase(
		[]ports.CodeGeneratorPort{goAdapter},
		nil,
		&mockRouter{strategy: ports.StrategyNative},
		&usecase.DefaultSchemaValidator{},
		&mockLogger{},
	)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageGo,
		Schema:      schema,
		OutputPath:  "./out",
		PackageName: "api",
	}

	result, err := uc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	if len(goAdapter.generated) != 1 {
		t.Fatalf("expected 1 generation call, got %d", len(goAdapter.generated))
	}
}

func TestCodeGeneratorUseCase_Generate_InvalidSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	goAdapter := &mockCodeGenAdapter{
		langs:    []ports.Language{ports.LanguageGo},
		strategy: ports.StrategyNative,
	}

	uc := usecase.NewCodeGeneratorUseCase(
		[]ports.CodeGeneratorPort{goAdapter},
		nil,
		&mockRouter{strategy: ports.StrategyNative},
		&usecase.DefaultSchemaValidator{},
		&mockLogger{},
	)

	req := &ports.GenerationRequest{
		Language:   ports.LanguageGo,
		Schema:     nil,
		OutputPath: "./out",
	}

	_, err := uc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected error for nil schema")
	}
}

func TestCodeGeneratorUseCase_Generate_NoAdapter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schema := &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")}

	uc := usecase.NewCodeGeneratorUseCase(
		[]ports.CodeGeneratorPort{},
		nil,
		&mockRouter{strategy: ports.StrategyNative},
		&usecase.DefaultSchemaValidator{},
		&mockLogger{},
	)

	req := &ports.GenerationRequest{
		Language:   ports.LanguageGo,
		Schema:     schema,
		OutputPath: "./out",
	}

	_, err := uc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected error for no adapter")
	}
}

func TestCodeGeneratorUseCase_Generate_AdapterError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schema := &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")}

	goAdapter := &mockCodeGenAdapter{
		langs:    []ports.Language{ports.LanguageGo},
		strategy: ports.StrategyNative,
		err:      errors.New("generation failed"),
	}

	uc := usecase.NewCodeGeneratorUseCase(
		[]ports.CodeGeneratorPort{goAdapter},
		nil,
		&mockRouter{strategy: ports.StrategyNative},
		&usecase.DefaultSchemaValidator{},
		&mockLogger{},
	)

	req := &ports.GenerationRequest{
		Language:   ports.LanguageGo,
		Schema:     schema,
		OutputPath: "./out",
	}

	_, err := uc.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected error from adapter")
	}
}

func TestCodeGeneratorUseCase_Generate_Fallback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schema := &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")}

	// No adapter for TypeScript, but fallback supports it.
	fallback := &mockCodeGenAdapter{
		langs:    []ports.Language{ports.LanguageTypeScript},
		strategy: ports.StrategyOpenAPI,
		result: &ports.GenerationResult{
			Files:    []ports.GeneratedFile{{Path: "client.ts", Content: []byte("// generated")}},
			Language: ports.LanguageTypeScript,
			Strategy: ports.StrategyOpenAPI,
		},
	}

	uc := usecase.NewCodeGeneratorUseCase(
		[]ports.CodeGeneratorPort{},
		fallback,
		&mockRouter{strategy: ports.StrategyOpenAPI},
		&usecase.DefaultSchemaValidator{},
		&mockLogger{},
	)

	req := &ports.GenerationRequest{
		Language:   ports.LanguageTypeScript,
		Schema:     schema,
		OutputPath: "./out",
	}

	result, err := uc.Generate(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil || len(result.Files) != 1 {
		t.Fatalf("expected fallback result with 1 file, got %+v", result)
	}
}

func TestDefaultSchemaValidator(t *testing.T) {
	t.Parallel()

	v := &usecase.DefaultSchemaValidator{}

	if err := v.Validate(nil); err == nil {
		t.Error("expected error for nil schema")
	}

	if err := v.Validate(&ports.OpenAPISchema{}); err == nil {
		t.Error("expected error for empty content")
	}

	if err := v.Validate(&ports.OpenAPISchema{Content: []byte("data")}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
