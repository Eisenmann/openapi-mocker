package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/example/openapi-mocker/internal/domain"
)

type MockService struct {
	mocks     MockRepository
	contracts ContractRepository
	providers ProviderRepository
	engine    ContractEngine
	llm       LLMGateway
}

func NewMockService(
	mocks MockRepository,
	contracts ContractRepository,
	providers ProviderRepository,
	engine ContractEngine,
	llm LLMGateway,
) *MockService {
	return &MockService{
		mocks:     mocks,
		contracts: contracts,
		providers: providers,
		engine:    engine,
		llm:       llm,
	}
}

func (s *MockService) List(projectID string) []*domain.MockRule {
	return s.mocks.ListMocks(projectID)
}

func (s *MockService) Create(m *domain.MockRule) (*domain.MockRule, error) {
	if err := validateJSONBody(m.Body); err != nil {
		return nil, err
	}
	if m.Scenario == "" {
		m.Scenario = "default"
	}
	return s.mocks.CreateMock(m), nil
}

func (s *MockService) Update(m *domain.MockRule) error {
	if err := validateJSONBody(m.Body); err != nil {
		return err
	}
	if m.Scenario == "" {
		m.Scenario = "default"
	}
	return s.mocks.UpdateMock(m)
}

func (s *MockService) Delete(id string) error {
	return s.mocks.DeleteMock(id)
}

func validateJSONBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return fmt.Errorf("mock body must be valid JSON: %w", err)
	}
	return nil
}

// GenerateBody asks the LLM to generate a mock body matching the endpoint's
// JSON Schema from the project's active contract, then immediately validates
// the result against that schema.
func (s *MockService) GenerateBody(
	ctx context.Context,
	projectID, path, method string,
	statusCode int,
	providerID, hints string,
) (body, warning string, err error) {
	if providerID == "" {
		return "", "", fmt.Errorf("providerId is required")
	}
	contract, err := s.contracts.GetActive(projectID)
	if err != nil {
		return "", "", fmt.Errorf("contract not loaded: %w", err)
	}
	provider, err := s.providers.GetProvider(providerID)
	if err != nil {
		return "", "", fmt.Errorf("LLM provider not found: %w", err)
	}

	status := strconv.Itoa(statusCode)
	if status == "0" {
		status = "200"
	}
	schemaJSON, err := s.engine.ResponseSchemaJSON([]byte(contract.Raw), method, path, status)
	if err != nil {
		return "", "", err
	}
	_, summary, found := s.engine.FindOperation([]byte(contract.Raw), method, path)
	if !found {
		return "", "", fmt.Errorf("operation %s %s not found in contract", method, path)
	}

	system := "You are generating test (mock) data for REST APIs. " +
		"Always respond with ONLY valid JSON without markdown, comments, or explanations. " +
		"Strictly follow the given JSON Schema: required fields, types, formats, enums."
	user := fmt.Sprintf(
		"Endpoint: %s %s (%s)\n\nResponse JSON Schema:\n%s\n\nAdditional data preferences: %s\n\n"+
			"Return a single JSON object (or array if the schema requires) with realistic values.",
		method, path, summary, schemaJSON, defaultIfEmpty(hints, "none"))

	resp, err := s.llm.Complete(
		ctx, provider,
		ChatRequest{
			SystemPrompt: system,
			UserPrompt:   user,
			Temperature:  0.7,
			MaxTokens:    2000,
		})
	if err != nil {
		return "", "", err
	}
	body = extractJSON(resp)

	if err := s.engine.ValidateResponseBody([]byte(contract.Raw), method, path, status, []byte(body)); err != nil {
		return body, "generated data does not fully match the response schema: " + err.Error(), nil
	}
	return body, "", nil
}

func defaultIfEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
