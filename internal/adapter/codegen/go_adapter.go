package codegen

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"strings"
	"text/template"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

// defaultPackageName is the package name used when none is specified.
const defaultPackageName = "api"

// GoNativeAdapter generates Go server and client code natively from an
// OpenAPI contract, without external tooling. It implements
// ports.CodeGeneratorPort.
type GoNativeAdapter struct {
	config GoConfig
}

// GoConfig holds configuration for the Go native adapter.
type GoConfig struct {
	ModulePath  string
	GoVersion   string
	TemplateDir string
}

// NewGoNativeAdapter creates a Go native adapter.
func NewGoNativeAdapter(config GoConfig) (*GoNativeAdapter, error) {
	if config.ModulePath == "" {
		config.ModulePath = "generated"
	}

	if config.GoVersion == "" {
		config.GoVersion = "1.21"
	}

	return &GoNativeAdapter{config: config}, nil
}

// Supports reports whether this adapter supports the given language.
func (a *GoNativeAdapter) Supports(lang ports.Language) bool {
	return lang == ports.LanguageGo
}

// Strategy returns the generation strategy used by this adapter.
func (a *GoNativeAdapter) Strategy() ports.GenerationStrategy {
	return ports.StrategyNative
}

// Generate produces Go server and client files from the OpenAPI schema.
func (a *GoNativeAdapter) Generate(
	ctx context.Context,
	req *ports.GenerationRequest,
) (*ports.GenerationResult, error) {
	doc, err := parseAndValidate(ctx, req.Schema.Content)
	if err != nil {
		return nil, err
	}

	pkgName := req.PackageName
	if pkgName == "" {
		pkgName = defaultPackageName
	}

	// Generate server files.
	serverFiles := generateGoServer(doc, pkgName)

	// Generate client file.
	clientFiles := generateGoClient(doc, pkgName)

	// Combine into GeneratedFile list.
	var files []ports.GeneratedFile
	for name, content := range serverFiles {
		files = append(files, ports.GeneratedFile{
			Path:    req.OutputPath + "/server/" + name,
			Content: []byte(content),
			IsNew:   true,
		})
	}

	for name, content := range clientFiles {
		files = append(files, ports.GeneratedFile{
			Path:    req.OutputPath + "/client/" + name,
			Content: []byte(content),
			IsNew:   true,
		})
	}

	// Generate go.mod.
	goMod := a.generateGoMod(req)

	files = append(files, ports.GeneratedFile{
		Path:    req.OutputPath + "/go.mod",
		Content: goMod,
		IsNew:   true,
	})

	return &ports.GenerationResult{
		Files:      files,
		Language:   ports.LanguageGo,
		Strategy:   ports.StrategyNative,
		Warnings:   []string{},
		CICDConfig: nil,
	}, nil
}

// generateGoMod produces a go.mod file for the generated project.
func (a *GoNativeAdapter) generateGoMod(_ *ports.GenerationRequest) []byte {
	modulePath := a.config.ModulePath
	if modulePath == "" {
		modulePath = "generated"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\n", modulePath)
	fmt.Fprintf(&b, "go %s\n", a.config.GoVersion)

	return []byte(b.String())
}

// formatGo formats Go source code, falling back to the raw bytes on error.
func formatGo(src []byte) []byte {
	formatted, err := format.Source(src)
	if err != nil {
		return src
	}

	return formatted
}

// executeTemplate renders a text/template with the given data.
func executeTemplate(name, tmpl string, data interface{}) ([]byte, error) {
	t, err := template.New(name).Parse(tmpl)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
