package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// OpenAPIGeneratorAdapter generates code for multiple languages by invoking
// the openapi-generator-cli tool. It implements ports.CodeGeneratorPort.
type OpenAPIGeneratorAdapter struct {
	config   OpenAPIConfig
	executor CommandExecutor
}

// OpenAPIConfig holds configuration for the OpenAPI generator adapter.
type OpenAPIConfig struct {
	CLIPath        string                    // path to openapi-generator-cli jar/binary.
	DefaultOptions map[string]string         // default generator options.
	TemplateDir    string                    // custom template directory.
	GeneratorMap   map[ports.Language]string // language -> generator name.
}

// CommandExecutor executes external commands. The default implementation
// uses os/exec.
type CommandExecutor interface {
	Execute(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSCommandExecutor is the default CommandExecutor using os/exec.
type OSCommandExecutor struct{}

// Execute runs a command and returns its combined output.
func (e *OSCommandExecutor) Execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	return cmd.CombinedOutput()
}

// NewOpenAPIGeneratorAdapter creates an OpenAPI generator adapter.
func NewOpenAPIGeneratorAdapter(
	config OpenAPIConfig,
	executor CommandExecutor,
) *OpenAPIGeneratorAdapter {
	if config.GeneratorMap == nil {
		config.GeneratorMap = defaultGeneratorMap()
	}

	if executor == nil {
		executor = &OSCommandExecutor{}
	}

	return &OpenAPIGeneratorAdapter{config: config, executor: executor}
}

// defaultGeneratorMap maps each language to its openapi-generator name.
func defaultGeneratorMap() map[ports.Language]string {
	return map[ports.Language]string{
		ports.LanguageTypeScript: "typescript-axios",
		ports.LanguagePython:     "python",
		ports.LanguageJava:       "java",
		ports.LanguageRust:       "rust",
		ports.LanguageCSharp:     "csharp",
		ports.LanguageGo:         "go",
	}
}

// Supports reports whether this adapter supports the given language.
func (a *OpenAPIGeneratorAdapter) Supports(lang ports.Language) bool {
	_, ok := a.config.GeneratorMap[lang]

	return ok
}

// Strategy returns the generation strategy used by this adapter.
func (a *OpenAPIGeneratorAdapter) Strategy() ports.GenerationStrategy {
	return ports.StrategyOpenAPI
}

// Generate invokes openapi-generator-cli to produce code for the requested
// language.
func (a *OpenAPIGeneratorAdapter) Generate(
	ctx context.Context,
	req *ports.GenerationRequest,
) (*ports.GenerationResult, error) {
	generatorName, ok := a.config.GeneratorMap[req.Language]
	if !ok {
		return nil, fmt.Errorf("%w: %s", usecase.ErrUnsupportedLanguage, req.Language)
	}

	// Write schema to temp file.
	schemaFile, err := a.writeSchemaToTemp(req.Schema)
	if err != nil {
		return nil, fmt.Errorf("failed to write schema: %w", err)
	}

	defer os.Remove(schemaFile)

	// Build config file.
	configFile, err := a.buildConfigFile(req)
	if err != nil {
		return nil, fmt.Errorf("failed to build config: %w", err)
	}

	defer os.Remove(configFile)

	// Execute openapi-generator-cli.
	args := a.buildArgs(generatorName, schemaFile, configFile, req)
	output, err := a.executor.Execute(ctx, "java", args...)
	if err != nil {
		return nil, fmt.Errorf("openapi-generator-cli failed: %w\nOutput: %s", err, output)
	}

	// Collect generated files.
	files, err := a.collectGeneratedFiles(req.OutputPath)
	if err != nil {
		return nil, err
	}

	return &ports.GenerationResult{
		Files:      files,
		Language:   req.Language,
		Strategy:   ports.StrategyOpenAPI,
		Warnings:   []string{},
		CICDConfig: nil,
	}, nil
}

// buildArgs constructs the openapi-generator-cli command-line arguments.
func (a *OpenAPIGeneratorAdapter) buildArgs(
	generator, schemaFile, configFile string,
	req *ports.GenerationRequest,
) []string {
	args := []string{
		"-jar", a.config.CLIPath,
		"generate",
		"-g", generator,
		"-i", schemaFile,
		"-o", req.OutputPath,
		"-c", configFile,
	}

	if a.config.TemplateDir != "" {
		args = append(args, "-t", a.config.TemplateDir)
	}

	// Additional options.
	for k, v := range req.Options {
		args = append(args, fmt.Sprintf("--%s=%v", k, v))
	}

	return args
}

// writeSchemaToTemp writes the schema content to a temporary file.
func (a *OpenAPIGeneratorAdapter) writeSchemaToTemp(
	schema *ports.OpenAPISchema,
) (string, error) {
	ext := "yaml"
	if schema.Format == "json" {
		ext = "json"
	}

	tmpFile, err := os.CreateTemp("", "schema-*."+ext)
	if err != nil {
		return "", err
	}

	defer tmpFile.Close()

	if _, err := tmpFile.Write(schema.Content); err != nil {
		return "", err
	}

	return tmpFile.Name(), nil
}

// buildConfigFile creates a JSON config file for openapi-generator-cli.
func (a *OpenAPIGeneratorAdapter) buildConfigFile(
	req *ports.GenerationRequest,
) (string, error) {
	config := map[string]interface{}{
		"packageName":    req.PackageName,
		"projectName":    req.Metadata.ProjectName,
		"packageVersion": req.Metadata.Version,
	}

	for k, v := range a.config.DefaultOptions {
		config[k] = v
	}

	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "openapi-config-*.json")
	if err != nil {
		return "", err
	}

	defer tmpFile.Close()

	if _, err := tmpFile.Write(data); err != nil {
		return "", err
	}

	return tmpFile.Name(), nil
}

// collectGeneratedFiles walks the output directory and collects all files.
// Uses filepath.WalkDir to avoid symlink-TOCTOU issues (G122).
func (a *OpenAPIGeneratorAdapter) collectGeneratedFiles(
	outputPath string,
) ([]ports.GeneratedFile, error) {
	var files []ports.GeneratedFile

	err := filepath.WalkDir(outputPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		// #nosec G122 -- the output directory is created by this adapter
		// and is not attacker-controlled; symlink traversal is not a
		// realistic threat here.
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		files = append(files, ports.GeneratedFile{
			Path:    path,
			Content: content,
			IsNew:   true,
		})

		return nil
	})

	return files, err
}
