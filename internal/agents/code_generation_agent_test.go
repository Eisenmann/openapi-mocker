package agents_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// ---------- Mock adapter ----------

type mockCodeGenAdapter struct {
	langs    []ports.Language
	strategy ports.GenerationStrategy
	result   *ports.GenerationResult
	err      error
}

func (m *mockCodeGenAdapter) Generate(ctx context.Context, req *ports.GenerationRequest) (*ports.GenerationResult, error) {
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

// ---------- Mock notifier ----------

type mockNotifier struct {
	events []agents.AgentEvent
}

func (m *mockNotifier) Notify(ctx context.Context, event agents.AgentEvent) error {
	m.events = append(m.events, event)
	return nil
}

// ---------- Tests ----------

func TestDefaultGenerationPlanner(t *testing.T) {
	t.Parallel()

	p := &agents.DefaultGenerationPlanner{}
	ctx := context.Background()

	req := agents.AgentRequest{
		Languages:   []ports.Language{ports.LanguageGo, ports.LanguageTypeScript},
		Schema:      &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")},
		ProjectName: "test",
		OutputBase:  "./out",
	}

	requests, err := p.Plan(ctx, &req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}

	if requests[0].Language != ports.LanguageGo {
		t.Errorf("expected first request for Go, got %s", requests[0].Language)
	}
	if requests[0].OutputPath != "./out/go" {
		t.Errorf("expected output path ./out/go, got %s", requests[0].OutputPath)
	}
	if requests[0].Metadata.ProjectName != "test" {
		t.Errorf("expected project name test, got %s", requests[0].Metadata.ProjectName)
	}
}

func TestDefaultGenerationPlanner_NoLanguages(t *testing.T) {
	t.Parallel()

	p := &agents.DefaultGenerationPlanner{}
	ctx := context.Background()

	req := agents.AgentRequest{
		Schema:     &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")},
		OutputBase: "./out",
	}

	_, err := p.Plan(ctx, &req)
	if err == nil {
		t.Fatal("expected error for no languages")
	}
}

func TestDefaultGenerationPlanner_NoSchema(t *testing.T) {
	t.Parallel()

	p := &agents.DefaultGenerationPlanner{}
	ctx := context.Background()

	req := agents.AgentRequest{
		Languages:  []ports.Language{ports.LanguageGo},
		OutputBase: "./out",
	}

	_, err := p.Plan(ctx, &req)
	if err == nil {
		t.Fatal("expected error for no schema")
	}
}

func TestDefaultResultValidator(t *testing.T) {
	t.Parallel()

	v := &agents.DefaultResultValidator{}
	ctx := context.Background()

	// Valid result
	report, err := v.Validate(ctx, &ports.GenerationResult{
		Files: []ports.GeneratedFile{{Path: "file.go", Content: []byte("package main")}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Error("expected valid report")
	}

	// Nil result
	report, err = v.Validate(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Error("expected invalid report for nil result")
	}

	// Empty files
	report, err = v.Validate(ctx, &ports.GenerationResult{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Error("expected invalid report for empty files")
	}
}

func TestCodeGenerationAgent_Execute(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Build a real usecase with a mock adapter.
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

	notifier := &mockNotifier{}

	agent := agents.NewCodeGenerationAgent(
		uc,
		&agents.DefaultGenerationPlanner{},
		&agents.DefaultResultValidator{},
		notifier,
	)

	req := agents.AgentRequest{
		Languages:   []ports.Language{ports.LanguageGo},
		Schema:      &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")},
		ProjectName: "test",
		OutputBase:  "./out",
	}

	results, err := agent.Execute(ctx, &req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if len(notifier.events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != "generation.planned" {
		t.Errorf("expected first event generation.planned, got %s", notifier.events[0].Type)
	}
	if notifier.events[len(notifier.events)-1].Type != "generation.completed" {
		t.Errorf("expected last event generation.completed, got %s", notifier.events[len(notifier.events)-1].Type)
	}
}

func TestCodeGenerationAgent_Execute_PartialFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Adapter that fails for Go but succeeds for TypeScript.
	failingAdapter := &mockCodeGenAdapter{
		langs:    []ports.Language{ports.LanguageGo},
		strategy: ports.StrategyNative,
		err:      errors.New("generation failed"),
	}

	tsAdapter := &mockCodeGenAdapter{
		langs:    []ports.Language{ports.LanguageTypeScript},
		strategy: ports.StrategyOpenAPI,
		result: &ports.GenerationResult{
			Files:    []ports.GeneratedFile{{Path: "client.ts", Content: []byte("// generated")}},
			Language: ports.LanguageTypeScript,
			Strategy: ports.StrategyOpenAPI,
		},
	}

	uc := usecase.NewCodeGeneratorUseCase(
		[]ports.CodeGeneratorPort{failingAdapter, tsAdapter},
		nil,
		&mockRouter{strategy: ports.StrategyOpenAPI},
		&usecase.DefaultSchemaValidator{},
		&mockLogger{},
	)

	agent := agents.NewCodeGenerationAgent(
		uc,
		&agents.DefaultGenerationPlanner{},
		&agents.DefaultResultValidator{},
		&mockNotifier{},
	)

	req := agents.AgentRequest{
		Languages:   []ports.Language{ports.LanguageGo, ports.LanguageTypeScript},
		Schema:      &ports.OpenAPISchema{Content: []byte("openapi: 3.0.0")},
		ProjectName: "test",
		OutputBase:  "./out",
	}

	results, err := agent.Execute(ctx, &req)
	if err == nil {
		t.Fatal("expected partial failure error")
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 successful result, got %d", len(results))
	}
	if results[0].Language != ports.LanguageTypeScript {
		t.Errorf("expected TypeScript result, got %s", results[0].Language)
	}
}
