package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

type ContractService struct {
	contracts  ContractRepository
	providers  ProviderRepository
	engine     ContractEngine
	llm        LLMGateway
	validators FormatValidators
	converter  ContractConverter
	detector   ChangeDetector
	alerts     AlertNotifier
}

// FormatValidators bundles the optional format-specific validation engines.
// A nil field means no engine wired: the format is then accepted without
// format-specific validation. OpenAPI validation never applies to GraphQL
// SDL or MCP manifests (running it on them is a category error, not a
// strictness choice), so nil engines simply skip that step.
type FormatValidators struct {
	GraphQL GraphQLEngine
	MCP     MCPEngine
	Proto   ProtoEngine
}

// NewContractService assembles the contract service. The variadic parameter
// is a practical compromise: most assembly points (and all existing tests)
// do not have format-specific engines, while the composition root passes
// both - the variadic keeps those call sites unchanged.
func NewContractService(
	contracts ContractRepository,
	providers ProviderRepository,
	engine ContractEngine,
	llm LLMGateway,
	validators ...FormatValidators,
) *ContractService {
	var fv FormatValidators
	if len(validators) > 0 {
		fv = validators[0]
	}

	return &ContractService{
		contracts:  contracts,
		providers:  providers,
		engine:     engine,
		llm:        llm,
		validators: fv,
		converter:  nil,
		detector:   nil,
		alerts:     nil,
	}
}

// WithChangeAlerts turns on breaking-change detection: the diff of two
// versions then lists the changes that affect clients, and publishing or
// rolling back to a version that breaks the previous one sends an alert.
// Either argument may be nil: without a notifier changes are only listed.
func (s *ContractService) WithChangeAlerts(detector ChangeDetector, alerts AlertNotifier) *ContractService {
	s.detector, s.alerts = detector, alerts

	return s
}

// WithConverter turns on conversion of Swagger 2.0 and OpenAPI 3.1 contracts:
// validating or publishing such a document converts it to OpenAPI 3.0 first.
func (s *ContractService) WithConverter(c ContractConverter) *ContractService {
	s.converter = c

	return s
}

// normalize converts a Swagger 2.0 / OpenAPI 3.1 document to OpenAPI 3.0 when
// a converter is wired. note says what was done ("" when nothing was).
func (s *ContractService) normalize(raw string) (converted, note string, err error) {
	if s.converter == nil || IsGraphQL(raw) || IsMCP(raw) || IsProto(raw) {
		return raw, "", nil
	}

	return s.converter.Normalize(raw)
}

// Export returns the active contract converted to target (ExportOpenAPI3,
// ExportOpenAPI31 or ExportSwagger2) and encoded as "json" or "yaml".
func (s *ContractService) Export(projectID, target, format string) (string, error) {
	switch target {
	case ExportOpenAPI3, ExportOpenAPI31, ExportSwagger2:
	default:
		return "", fmt.Errorf("%w: unknown format %q (use openapi3, openapi31 or swagger2)", ErrInvalidExport, target)
	}

	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return "", err
	}

	if c.Format != FormatJSON && c.Format != FormatYAML {
		return "", fmt.Errorf("%w: only OpenAPI contracts can be converted", ErrInvalidExport)
	}

	if s.converter == nil {
		return c.Raw, nil
	}

	return s.converter.Export(c.Raw, target, format)
}

func (s *ContractService) GetActive(projectID string) (*domain.Contract, error) {
	return s.contracts.GetActive(projectID)
}

func (s *ContractService) GetVersion(projectID string, version int) (*domain.Contract, error) {
	return s.contracts.GetVersion(projectID, version)
}

func (s *ContractService) ListVersions(projectID string) []*domain.Contract {
	return s.contracts.ListVersions(projectID)
}

func (s *ContractService) Validate(raw string) ValidationResult {
	converted, note, err := s.normalize(raw)
	if err != nil {
		return ValidationResult{Valid: false, Errors: []string{err.Error()}}
	}

	res := s.validateNormalized(converted)
	res.Note = note

	return res
}

func (s *ContractService) validateNormalized(raw string) ValidationResult {
	switch detectFormat(raw) {
	case FormatProto:
		if s.validators.Proto == nil {
			return ValidationResult{Valid: true}
		}

		res := s.validators.Proto.Validate([]byte(raw))

		return ValidationResult{Valid: res.Valid, Errors: res.Errors, OpCount: res.Methods}
	case FormatGraphQL:
		if s.validators.GraphQL == nil {
			return ValidationResult{Valid: true}
		}

		res := s.validators.GraphQL.Validate([]byte(raw))

		return ValidationResult{
			Valid:   res.Valid,
			Errors:  res.Errors,
			OpCount: res.QueryFields + res.MutationFields,
		}
	case FormatMCP:
		if s.validators.MCP == nil {
			return ValidationResult{Valid: true}
		}

		res := s.validators.MCP.Validate([]byte(raw))

		return ValidationResult{
			Valid:   res.Valid,
			Errors:  res.Errors,
			OpCount: res.ToolCount + res.ResourceCount + res.PromptCount,
		}
	default:
		return s.engine.Validate([]byte(raw))
	}
}

// Publish validates and saves a new version of the contract. Publishing makes
// endpoints live IMMEDIATELY: the mock server (see MockServingService) always
// serves the latest saved version; there is no separate "deploy" step.
func (s *ContractService) Publish(projectID, raw, source string) (*domain.Contract, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrContractEmpty
	}

	raw, note, err := s.normalize(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrContractInvalid, err)
	}

	format := detectFormat(raw)

	err = s.validateFormat(format, raw)
	if err != nil {
		return nil, err
	}

	if source == "" {
		source = "manual"
	}

	if note != "" {
		source += " (" + note + ")"
	}

	prev, _ := s.contracts.GetActive(projectID)
	added := s.contracts.AddVersion(projectID, format, raw, source)
	s.alertOnBreaking(prev, added)

	return added, nil
}

// validateFormat runs the validation matching the contract format: OpenAPI
// documents (json/yaml) through the OpenAPI engine, GraphQL SDL through the
// GraphQL engine, MCP manifests through the MCP engine. A format whose
// engine is not wired is accepted as-is.
func (s *ContractService) validateFormat(format, raw string) error {
	switch format {
	case FormatProto:
		if s.validators.Proto == nil {
			return nil
		}

		return s.validators.Proto.ParseAndValidate([]byte(raw))
	case FormatGraphQL:
		if s.validators.GraphQL == nil {
			return nil
		}

		return s.validators.GraphQL.ParseAndValidate([]byte(raw))
	case FormatMCP:
		if s.validators.MCP == nil {
			return nil
		}

		return s.validators.MCP.ParseAndValidate([]byte(raw))
	default:
		return s.engine.ParseAndValidate([]byte(raw))
	}
}

// detectFormat classifies a raw contract by its content: GraphQL SDL, MCP
// manifest, JSON or YAML (OpenAPI documents are the JSON/YAML cases). The
// order matters: GraphQL SDL is never JSON, and an MCP manifest is JSON but
// not an OpenAPI document.
func detectFormat(raw string) string {
	switch {
	case IsProto(raw):
		return FormatProto
	case IsGraphQL(raw):
		return FormatGraphQL
	case IsMCP(raw):
		return FormatMCP
	case strings.HasPrefix(strings.TrimSpace(raw), "{"):
		return FormatJSON
	default:
		return FormatYAML
	}
}

// Rollback re-publishes an old version (creates a new history entry with its
// contents). History stays linear and complete; nothing is rewritten.
func (s *ContractService) Rollback(projectID string, version int) (*domain.Contract, error) {
	old, err := s.contracts.GetVersion(projectID, version)
	if err != nil {
		return nil, err
	}

	prev, _ := s.contracts.GetActive(projectID)
	added := s.contracts.AddVersion(projectID, old.Format, old.Raw, fmt.Sprintf("rollback-to-v%d", version))
	s.alertOnBreaking(prev, added)

	return added, nil
}

// ContractDiff is the result of comparing two versions.
type ContractDiff struct {
	FromVersion int        `json:"fromVersion"`
	ToVersion   int        `json:"toVersion"`
	Lines       []DiffLine `json:"diff"`
	// Breaking lists the changes that affect clients (breaking changes and
	// warnings); empty for formats other than OpenAPI.
	Breaking []domain.ContractChange `json:"breaking"`
}

// Diff compares version fromVersion against toVersion; if toVersion == nil,
// the comparison is against the current active version.
func (s *ContractService) Diff(projectID string, fromVersion int, toVersion *int) (ContractDiff, error) {
	from, err := s.contracts.GetVersion(projectID, fromVersion)
	if err != nil {
		return ContractDiff{}, fmt.Errorf("from version not found: %w", err)
	}

	var to *domain.Contract
	if toVersion == nil {
		to, err = s.contracts.GetActive(projectID)
	} else {
		to, err = s.contracts.GetVersion(projectID, *toVersion)
	}

	if err != nil {
		return ContractDiff{}, err
	}

	return ContractDiff{
		FromVersion: from.Version,
		ToVersion:   to.Version,
		Lines:       s.engine.Diff(from.Raw, to.Raw),
		Breaking:    s.changes(from, to),
	}, nil
}

func (s *ContractService) ListEndpoints(projectID string) ([]Endpoint, error) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return nil, err
	}

	return s.engine.ListEndpoints([]byte(c.Raw))
}

// GenerateFromDescription asks the LLM to build a contract from a free-text
// API description. The returned raw is NOT published automatically — the user
// must explicitly save it via Publish (giving a chance to review and fix the
// generated output before it goes live).
func (s *ContractService) GenerateFromDescription(
	ctx context.Context,
	providerID, description string,
) (raw, validationWarning string, err error) {
	if providerID == "" || strings.TrimSpace(description) == "" {
		return "", "", ErrProviderAndDescRequired
	}

	provider, err := s.providers.GetProvider(providerID)
	if err != nil {
		return "", "", fmt.Errorf("LLM provider not found: %w", err)
	}

	system := "You are an experienced API architect. Generate complete and valid " +
		"OpenAPI 3.0.3 contracts in JSON format. " +
		"Respond ONLY with a valid OpenAPI JSON document (openapi, info, paths, components) " +
		"without markdown or explanations. " +
		"Always specify schemas for request/response bodies, field examples (example), and correct HTTP status codes."
	user := "Describe and generate an OpenAPI 3.0.3 contract for the following API:\n\n" + description

	resp, err := s.llm.Complete(
		ctx,
		provider,
		ChatRequest{
			SystemPrompt: system,
			UserPrompt:   user,
			Temperature:  ContractTemperature,
			MaxTokens:    ContractMaxTokens,
		})
	if err != nil {
		return "", "", err
	}

	raw = extractJSON(resp)
	err = s.engine.ParseAndValidate([]byte(raw))
	if err != nil {
		return raw,
			"LLM returned a contract that is not valid OpenAPI 3.x — please review and fix manually: " + err.Error(),
			nil
	}

	return raw, "", nil
}
