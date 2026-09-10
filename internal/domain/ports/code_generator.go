// Package ports contains the domain-level ports (interfaces) for the
// multi-language code generation system. Following the Clean Architecture
// pattern of the rest of the application, these interfaces are defined in
// the domain layer and implemented by adapters in internal/adapter/*.
package ports

import "context"

// Language identifies a target programming language for code generation.
type Language string

const (
	LanguageGo         Language = "go"
	LanguageTypeScript Language = "typescript"
	LanguagePython     Language = "python"
	LanguageJava       Language = "java"
	LanguageRust       Language = "rust"
	LanguageCSharp     Language = "csharp"
)

// GenerationStrategy identifies the mechanism used to generate code.
type GenerationStrategy string

const (
	StrategyNative  GenerationStrategy = "native"  // Go native.
	StrategyOpenAPI GenerationStrategy = "openapi" // openapi-generator-cli.
	StrategyCICD    GenerationStrategy = "cicd"    // CI/CD pipeline step.
	StrategyAI      GenerationStrategy = "ai"      // AI-powered generation.
)

// CodeGeneratorPort is the port that all code generation adapters implement.
// Each adapter supports one or more languages and uses a specific strategy.
type CodeGeneratorPort interface {
	Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error)
	Supports(language Language) bool
	Strategy() GenerationStrategy
}

// GenerationRequest describes a single code generation task.
type GenerationRequest struct {
	Language    Language               `json:"language"`
	Schema      *OpenAPISchema         `json:"schema"`
	Template    *TemplateConfig        `json:"template,omitempty"`
	Options     map[string]interface{} `json:"options,omitempty"`
	OutputPath  string                 `json:"output_path"`
	PackageName string                 `json:"package_name"`
	Metadata    GenerationMetadata     `json:"metadata"`
}

// GenerationResult is the outcome of a code generation task.
type GenerationResult struct {
	Files      []GeneratedFile    `json:"files"`
	Language   Language           `json:"language"`
	Strategy   GenerationStrategy `json:"strategy"`
	Warnings   []string           `json:"warnings,omitempty"`
	CICDConfig *CICDConfig        `json:"cicd_config,omitempty"`
}

// GeneratedFile is a single file produced by a code generator.
type GeneratedFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	IsNew   bool   `json:"is_new"`
}

// OpenAPISchema wraps the raw OpenAPI contract content.
type OpenAPISchema struct {
	Version     string                 `json:"version"`
	Content     []byte                 `json:"content"`
	Format      string                 `json:"format"` // yaml/json.
	Definitions map[string]interface{} `json:"definitions"`
}

// CICDConfig describes a CI/CD pipeline step for code generation.
type CICDConfig struct {
	Platform   string            `json:"platform"` // github/gitlab/jenkins.
	StepConfig map[string]string `json:"step_config"`
	Commands   []string          `json:"commands"`
}

// GenerationMetadata carries project-level metadata for generation.
type GenerationMetadata struct {
	ProjectName string            `json:"project_name"`
	Version     string            `json:"version"`
	Authors     []string          `json:"authors"`
	Tags        map[string]string `json:"tags"`
}

// TemplateConfig describes a custom template override for generation.
type TemplateConfig struct {
	Dir    string            `json:"dir"`
	Values map[string]string `json:"values"`
}
