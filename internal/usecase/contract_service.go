package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

type ContractService struct {
	contracts ContractRepository
	providers ProviderRepository
	engine    ContractEngine
	llm       LLMGateway
}

func NewContractService(
	contracts ContractRepository,
	providers ProviderRepository,
	engine ContractEngine,
	llm LLMGateway,
) *ContractService {
	return &ContractService{
		contracts: contracts,
		providers: providers,
		engine:    engine,
		llm:       llm,
	}
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
	return s.engine.Validate([]byte(raw))
}

// Publish validates and saves a new version of the contract. Publishing makes
// endpoints live IMMEDIATELY: the mock server (see MockServingService) always
// serves the latest saved version; there is no separate "deploy" step.
func (s *ContractService) Publish(projectID, raw, source string) (*domain.Contract, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("contract is empty")
	}
	if err := s.engine.ParseAndValidate([]byte(raw)); err != nil {
		return nil, err
	}
	format := "yaml"
	if IsGraphQL(raw) {
		format = FormatGraphQL
	} else if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		format = "json"
	}
	if source == "" {
		source = "manual"
	}
	return s.contracts.AddVersion(projectID, format, raw, source), nil
}

// Rollback re-publishes an old version (creates a new history entry with its
// contents). History stays linear and complete; nothing is rewritten.
func (s *ContractService) Rollback(projectID string, version int) (*domain.Contract, error) {
	old, err := s.contracts.GetVersion(projectID, version)
	if err != nil {
		return nil, err
	}
	return s.contracts.AddVersion(projectID, old.Format, old.Raw, fmt.Sprintf("rollback-to-v%d", version)), nil
}

// ContractDiff is the result of comparing two versions.
type ContractDiff struct {
	FromVersion int        `json:"fromVersion"`
	ToVersion   int        `json:"toVersion"`
	Lines       []DiffLine `json:"diff"`
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
		return "", "", errors.New("providerId and description are required")
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
			Temperature:  0.4,
			MaxTokens:    4000,
		})
	if err != nil {
		return "", "", err
	}
	raw = extractJSON(resp)
	if err := s.engine.ParseAndValidate([]byte(raw)); err != nil {
		return raw,
			"LLM returned a contract that is not valid OpenAPI 3.x — please review and fix manually: " + err.Error(),
			nil
	}
	return raw, "", nil
}
