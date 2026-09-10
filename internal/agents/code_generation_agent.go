// Package agents contains the AI agent layer that orchestrates multi-language
// code generation: planning, execution, and validation.
package agents

import (
	"context"
	"fmt"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// CodeGenerationAgent orchestrates the full code generation workflow:
// planning, execution, and validation.
type CodeGenerationAgent struct {
	useCase   *usecase.CodeGeneratorUseCase
	planner   GenerationPlanner
	validator ResultValidator
	notifier  AgentNotifier
}

// GenerationPlanner plans the generation tasks from an agent request.
type GenerationPlanner interface {
	Plan(ctx context.Context, request *AgentRequest) ([]*ports.GenerationRequest, error)
}

// ResultValidator validates a generation result.
type ResultValidator interface {
	Validate(ctx context.Context, result *ports.GenerationResult) (ValidationReport, error)
}

// AgentNotifier emits events during the generation workflow.
type AgentNotifier interface {
	Notify(ctx context.Context, event AgentEvent) error
}

// AgentRequest is the top-level request to the code generation agent.
type AgentRequest struct {
	Languages   []ports.Language         `json:"languages"`
	Schema      *ports.OpenAPISchema     `json:"schema"`
	ProjectName string                   `json:"project_name"`
	OutputBase  string                   `json:"output_base"`
	Strategy    ports.GenerationStrategy `json:"strategy,omitempty"`
}

// AgentEvent is a notification emitted during generation.
type AgentEvent struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// ValidationReport is the result of validating a generation result.
type ValidationReport struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// NewCodeGenerationAgent creates a code generation agent.
func NewCodeGenerationAgent(
	useCase *usecase.CodeGeneratorUseCase,
	planner GenerationPlanner,
	validator ResultValidator,
	notifier AgentNotifier,
) *CodeGenerationAgent {
	return &CodeGenerationAgent{
		useCase:   useCase,
		planner:   planner,
		validator: validator,
		notifier:  notifier,
	}
}

// Execute runs the full generation workflow for all requested languages.
func (a *CodeGenerationAgent) Execute(
	ctx context.Context,
	request *AgentRequest,
) ([]*ports.GenerationResult, error) {
	// Step 1: Plan generation tasks.
	requests, err := a.planner.Plan(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	if a.notifier != nil {
		_ = a.notifier.Notify(ctx, AgentEvent{
			Type:    "generation.planned",
			Payload: map[string]int{"tasks": len(requests)},
		})
	}

	// Step 2: Execute generation for each language.
	var results []*ports.GenerationResult

	var errs []error

	for _, req := range requests {
		result, err := a.useCase.Generate(ctx, req)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed for %s: %w", req.Language, err))

			if a.notifier != nil {
				_ = a.notifier.Notify(ctx, AgentEvent{
					Type:    "generation.failed",
					Payload: map[string]string{"language": string(req.Language), "error": err.Error()},
				})
			}

			continue
		}

		// Step 3: Validate result.
		report, err := a.validator.Validate(ctx, result)
		if err != nil || !report.Valid {
			errs = append(errs, fmt.Errorf("%w for %s: %v", usecase.ErrValidationFailed, req.Language, report.Errors))

			continue
		}

		results = append(results, result)

		if a.notifier != nil {
			_ = a.notifier.Notify(ctx, AgentEvent{
				Type: "generation.completed",
				Payload: map[string]interface{}{
					"language": req.Language,
					"files":    len(result.Files),
					"strategy": result.Strategy,
				},
			})
		}
	}

	if len(errs) > 0 {
		return results, fmt.Errorf("%w: %v", usecase.ErrPartialFailure, errs)
	}

	return results, nil
}

// DefaultGenerationPlanner creates one generation request per language.
type DefaultGenerationPlanner struct{}

// Plan builds a GenerationRequest for each requested language.
func (p *DefaultGenerationPlanner) Plan(
	_ context.Context,
	request *AgentRequest,
) ([]*ports.GenerationRequest, error) {
	if len(request.Languages) == 0 {
		return nil, usecase.ErrNoLanguagesRequested
	}

	if request.Schema == nil {
		return nil, usecase.ErrSchemaRequired
	}

	requests := make([]*ports.GenerationRequest, 0, len(request.Languages))
	for _, lang := range request.Languages {
		outputPath := request.OutputBase + "/" + string(lang)
		req := &ports.GenerationRequest{
			Language:    lang,
			Schema:      request.Schema,
			OutputPath:  outputPath,
			PackageName: string(lang),
			Metadata: ports.GenerationMetadata{
				ProjectName: request.ProjectName,
			},
		}

		if request.Strategy != "" {
			req.Options = map[string]interface{}{
				"strategy": request.Strategy,
			}
		}

		requests = append(requests, req)
	}

	return requests, nil
}

// DefaultResultValidator checks that a generation result has files.
type DefaultResultValidator struct{}

// Validate checks that the result contains at least one file.
func (v *DefaultResultValidator) Validate(
	_ context.Context,
	result *ports.GenerationResult,
) (ValidationReport, error) {
	if result == nil {
		return ValidationReport{Valid: false, Errors: []string{"result is nil"}, Warnings: []string{}}, nil
	}

	if len(result.Files) == 0 {
		return ValidationReport{Valid: false, Errors: []string{"no files generated"}, Warnings: []string{}}, nil
	}

	return ValidationReport{Valid: true, Errors: []string{}, Warnings: []string{}}, nil
}
