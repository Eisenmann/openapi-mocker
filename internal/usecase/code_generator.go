package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

// Logger is a minimal logging interface used by the usecase layer. The
// concrete implementation is provided by the composition root.
type Logger interface {
	Info(msg string, keyvals ...interface{})
	Error(msg string, keyvals ...interface{})
}

// LanguageRouter selects the generation strategy for a given language and
// request. The default implementation lives in internal/adapter/router.
type LanguageRouter interface {
	SelectStrategy(language ports.Language, req *ports.GenerationRequest) ports.GenerationStrategy
}

// SchemaValidator validates an OpenAPI schema before generation.
type SchemaValidator interface {
	Validate(schema *ports.OpenAPISchema) error
}

// Sentinel errors for code generation failures.
var (
	ErrNoAdapterFound       = errors.New("no adapter found for language")
	ErrSchemaNil            = errors.New("schema is nil")
	ErrSchemaEmpty          = errors.New("schema content is empty")
	ErrSchemaRequired       = errors.New("schema is required")
	ErrNoLanguagesRequested = errors.New("no languages requested")
	ErrUnsupportedLanguage  = errors.New("unsupported language")
	ErrUnsupportedPlatform  = errors.New("unsupported CI/CD platform")
	ErrValidationFailed     = errors.New("validation failed")
	ErrPartialFailure       = errors.New("partial failure")
)

// CodeGeneratorUseCase orchestrates multi-language code generation by
// selecting the appropriate adapter for each language and strategy.
type CodeGeneratorUseCase struct {
	adapters  map[ports.Language][]ports.CodeGeneratorPort
	fallback  ports.CodeGeneratorPort
	router    LanguageRouter
	validator SchemaValidator
	logger    Logger
}

// NewCodeGeneratorUseCase builds the use case from a list of adapters.
// Each adapter is registered for every language it supports.
func NewCodeGeneratorUseCase(
	adapters []ports.CodeGeneratorPort,
	fallback ports.CodeGeneratorPort,
	router LanguageRouter,
	validator SchemaValidator,
	logger Logger,
) *CodeGeneratorUseCase {
	adapterMap := make(map[ports.Language][]ports.CodeGeneratorPort)

	for _, adapter := range adapters {
		for _, lang := range allLanguages() {
			if adapter.Supports(lang) {
				adapterMap[lang] = append(adapterMap[lang], adapter)
			}
		}
	}

	return &CodeGeneratorUseCase{
		adapters:  adapterMap,
		fallback:  fallback,
		router:    router,
		validator: validator,
		logger:    logger,
	}
}

// Generate validates the schema, selects the appropriate adapter, and
// produces the generated code.
func (uc *CodeGeneratorUseCase) Generate(
	ctx context.Context,
	req *ports.GenerationRequest,
) (*ports.GenerationResult, error) {
	// 1. Validate schema.
	err := uc.validator.Validate(req.Schema)
	if err != nil {
		return nil, fmt.Errorf("schema validation failed: %w", err)
	}

	// 2. Select adapter by strategy.
	adapter, err := uc.selectAdapter(req.Language, req)
	if err != nil {
		return nil, err
	}

	// 3. Generate code.
	result, err := adapter.Generate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("generation failed for %s: %w", req.Language, err)
	}

	if uc.logger != nil {
		uc.logger.Info("code generated",
			"language", req.Language,
			"strategy", adapter.Strategy(),
			"files", len(result.Files),
		)
	}

	return result, nil
}

// selectAdapter picks the adapter that matches the preferred strategy for
// the given language, falling back to the first available adapter.
func (uc *CodeGeneratorUseCase) selectAdapter(
	lang ports.Language,
	req *ports.GenerationRequest,
) (ports.CodeGeneratorPort, error) {
	adapters, ok := uc.adapters[lang]
	if !ok || len(adapters) == 0 {
		if uc.fallback != nil {
			return uc.fallback, nil
		}

		return nil, fmt.Errorf("%w: %s", ErrNoAdapterFound, lang)
	}

	targetStrategy := uc.router.SelectStrategy(lang, req)

	// Find adapter matching preferred strategy.
	for _, a := range adapters {
		if a.Strategy() == targetStrategy {
			return a, nil
		}
	}

	// Return first available as fallback.
	return adapters[0], nil
}

// allLanguages returns the list of supported languages.
func allLanguages() []ports.Language {
	return []ports.Language{
		ports.LanguageGo,
		ports.LanguageTypeScript,
		ports.LanguagePython,
		ports.LanguageJava,
		ports.LanguageRust,
		ports.LanguageCSharp,
	}
}

// DefaultSchemaValidator is a simple validator that checks the schema is
// non-nil and has content.
type DefaultSchemaValidator struct{}

// Validate checks that the schema is present and has content.
func (v *DefaultSchemaValidator) Validate(schema *ports.OpenAPISchema) error {
	if schema == nil {
		return ErrSchemaNil
	}

	if len(schema.Content) == 0 {
		return ErrSchemaEmpty
	}

	return nil
}

// SortResults orders generation results by language for deterministic output.
func SortResults(results []*ports.GenerationResult) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Language < results[j].Language
	})
}
