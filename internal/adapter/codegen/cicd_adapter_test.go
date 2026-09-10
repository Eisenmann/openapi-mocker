package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestCICDAdapter_Supports(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformGitHubActions)

	if !a.Supports(ports.LanguageGo) {
		t.Error("expected Go to be supported")
	}
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
}

func TestCICDAdapter_Strategy(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformGitHubActions)

	if a.Strategy() != ports.StrategyCICD {
		t.Errorf("expected StrategyCICD, got %s", a.Strategy())
	}
}

func TestCICDAdapter_Generate_GitHubActions(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformGitHubActions)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageTypeScript,
		PackageName: "api",
		OutputPath:  "./generated/client",
		Metadata: ports.GenerationMetadata{
			ProjectName: "test-project",
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

	if result.Strategy != ports.StrategyCICD {
		t.Errorf("expected StrategyCICD, got %s", result.Strategy)
	}

	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	file := result.Files[0]
	if file.Path != ".github/workflows/codegen.yml" {
		t.Errorf("expected .github/workflows/codegen.yml, got %s", file.Path)
	}

	content := string(file.Content)
	if !strings.Contains(content, "name: Code Generation - typescript") {
		t.Error("missing workflow name")
	}
	if !strings.Contains(content, "generate-typescript-client") {
		t.Error("missing job name")
	}
	if !strings.Contains(content, "typescript-axios") {
		t.Error("missing generator name")
	}
	if !strings.Contains(content, "./generated/client") {
		t.Error("missing output path")
	}

	// Verify CICDConfig
	if result.CICDConfig == nil {
		t.Fatal("expected CICDConfig")
	}
	if result.CICDConfig.Platform != "github" {
		t.Errorf("expected platform github, got %s", result.CICDConfig.Platform)
	}
	if len(result.CICDConfig.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(result.CICDConfig.Commands))
	}
	if !strings.Contains(result.CICDConfig.Commands[0], "typescript-axios") {
		t.Error("expected typescript-axios in command")
	}
	if result.CICDConfig.StepConfig["generator"] != "typescript-axios" {
		t.Errorf("expected generator typescript-axios, got %s", result.CICDConfig.StepConfig["generator"])
	}
	if result.CICDConfig.StepConfig["language"] != "typescript" {
		t.Errorf("expected language typescript, got %s", result.CICDConfig.StepConfig["language"])
	}

	// Verify warnings
	if len(result.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(result.Warnings))
	}
}

func TestCICDAdapter_Generate_GitLabCI(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformGitLabCI)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageGo,
		PackageName: "api",
		OutputPath:  "./generated/go",
		Metadata: ports.GenerationMetadata{
			ProjectName: "test-project",
		},
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	file := result.Files[0]
	if file.Path != ".gitlab-ci.yml" {
		t.Errorf("expected .gitlab-ci.yml, got %s", file.Path)
	}

	content := string(file.Content)
	if !strings.Contains(content, "stages:") {
		t.Error("missing stages")
	}
	if !strings.Contains(content, "generate-go:") {
		t.Error("missing job name")
	}
	if !strings.Contains(content, "go") {
		t.Error("missing generator name")
	}
}

func TestCICDAdapter_Generate_Jenkins(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformJenkins)

	req := &ports.GenerationRequest{
		Language:    ports.LanguageJava,
		PackageName: "api",
		OutputPath:  "./generated/java",
		Metadata: ports.GenerationMetadata{
			ProjectName: "test-project",
		},
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	file := result.Files[0]
	if file.Path != "Jenkinsfile" {
		t.Errorf("expected Jenkinsfile, got %s", file.Path)
	}
}

func TestCICDAdapter_Generate_CircleCI(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformCircleCI)

	req := &ports.GenerationRequest{
		Language:    ports.LanguagePython,
		PackageName: "api",
		OutputPath:  "./generated/python",
		Metadata: ports.GenerationMetadata{
			ProjectName: "test-project",
		},
	}

	result, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	file := result.Files[0]
	if file.Path != ".circleci/config.yml" {
		t.Errorf("expected .circleci/config.yml, got %s", file.Path)
	}
}

func TestCICDAdapter_Generate_UnsupportedPlatform(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter("unknown")

	req := &ports.GenerationRequest{
		Language:    ports.LanguageGo,
		PackageName: "api",
		OutputPath:  "./out",
	}

	_, err := a.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}

func TestCICDAdapter_Generate_AllLanguages(t *testing.T) {
	t.Parallel()

	a := NewCICDAdapter(PlatformGitHubActions)

	languages := []ports.Language{
		ports.LanguageGo,
		ports.LanguageTypeScript,
		ports.LanguagePython,
		ports.LanguageJava,
		ports.LanguageRust,
		ports.LanguageCSharp,
	}

	for _, lang := range languages {
		t.Run(string(lang), func(t *testing.T) {
			t.Parallel()

			req := &ports.GenerationRequest{
				Language:    lang,
				PackageName: "api",
				OutputPath:  "./out",
			}

			result, err := a.Generate(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.CICDConfig == nil {
				t.Fatal("expected CICDConfig")
			}

			expectedGenerator := defaultGeneratorMap()[lang]
			if result.CICDConfig.StepConfig["generator"] != expectedGenerator {
				t.Errorf("expected generator %s, got %s", expectedGenerator, result.CICDConfig.StepConfig["generator"])
			}
		})
	}
}

func TestLoadCICDTemplates(t *testing.T) {
	t.Parallel()

	templates := loadCICDTemplates()

	if len(templates) != 4 {
		t.Fatalf("expected 4 templates, got %d", len(templates))
	}

	if _, ok := templates[PlatformGitHubActions]; !ok {
		t.Error("missing GitHub Actions template")
	}

	if _, ok := templates[PlatformGitLabCI]; !ok {
		t.Error("missing GitLab CI template")
	}

	if _, ok := templates[PlatformJenkins]; !ok {
		t.Error("missing Jenkins template")
	}

	if _, ok := templates[PlatformCircleCI]; !ok {
		t.Error("missing CircleCI template")
	}
}
