package usecase_test

import (
	"context"
	"errors"
	"sync"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// ---------- Mock repositories ----------.

type mockProjectRepo struct {
	mu       sync.Mutex
	projects map[string]*domain.Project
	order    []string
}

func newMockProjectRepo() *mockProjectRepo {
	return &mockProjectRepo{projects: map[string]*domain.Project{}}
}

func (m *mockProjectRepo) Create(name, description string) *domain.Project {
	m.mu.Lock()
	defer m.mu.Unlock()

	p := &domain.Project{ID: "p" + name, Name: name, Description: description}
	m.projects[p.ID] = p
	m.order = append(m.order, p.ID)

	return p
}

func (m *mockProjectRepo) List() []*domain.Project {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*domain.Project, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.projects[id])
	}

	return out
}

func (m *mockProjectRepo) Get(id string) (*domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.projects[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return p, nil
}

func (m *mockProjectRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.projects[id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.projects, id)

	return nil
}

type mockContractRepo struct {
	mu        sync.Mutex
	contracts map[string][]*domain.Contract
}

func newMockContractRepo() *mockContractRepo {
	return &mockContractRepo{contracts: map[string][]*domain.Contract{}}
}

func (m *mockContractRepo) AddVersion(projectID, format, raw, source string) *domain.Contract {
	m.mu.Lock()
	defer m.mu.Unlock()

	history := m.contracts[projectID]
	c := &domain.Contract{
		ID:        "c" + projectID + string(rune(len(history)+1)),
		ProjectID: projectID,
		Format:    format,
		Raw:       raw,
		Version:   len(history) + 1,
		Source:    source,
	}
	m.contracts[projectID] = append(history, c)

	return c
}

func (m *mockContractRepo) GetActive(projectID string) (*domain.Contract, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	history := m.contracts[projectID]
	if len(history) == 0 {
		return nil, domain.ErrNotFound
	}

	return history[len(history)-1], nil
}

func (m *mockContractRepo) GetVersion(projectID string, version int) (*domain.Contract, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.contracts[projectID] {
		if c.Version == version {
			return c, nil
		}
	}

	return nil, domain.ErrNotFound
}

func (m *mockContractRepo) ListVersions(projectID string) []*domain.Contract {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]*domain.Contract{}, m.contracts[projectID]...)
}

type mockMockRepo struct {
	mu    sync.Mutex
	mocks map[string]*domain.MockRule
}

func newMockMockRepo() *mockMockRepo {
	return &mockMockRepo{mocks: map[string]*domain.MockRule{}}
}

func (m *mockMockRepo) CreateMock(mr *domain.MockRule) *domain.MockRule {
	m.mu.Lock()
	defer m.mu.Unlock()

	mr.ID = "mock" + mr.Path + mr.Method + mr.Scenario
	m.mocks[mr.ID] = mr

	return mr
}

func (m *mockMockRepo) UpdateMock(mr *domain.MockRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.mocks[mr.ID]; !ok {
		return domain.ErrNotFound
	}

	m.mocks[mr.ID] = mr

	return nil
}

func (m *mockMockRepo) DeleteMock(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.mocks[id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.mocks, id)

	return nil
}

func (m *mockMockRepo) ListMocks(projectID string) []*domain.MockRule {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*domain.MockRule, 0)

	for _, mr := range m.mocks {
		if mr.ProjectID == projectID {
			out = append(out, mr)
		}
	}

	return out
}

func (m *mockMockRepo) GetMock(id string) (*domain.MockRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mr, ok := m.mocks[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return mr, nil
}

type mockProviderRepo struct {
	mu        sync.Mutex
	providers map[string]*domain.LLMProvider
}

func newMockProviderRepo() *mockProviderRepo {
	return &mockProviderRepo{providers: map[string]*domain.LLMProvider{}}
}

func (m *mockProviderRepo) CreateProvider(p *domain.LLMProvider) *domain.LLMProvider {
	m.mu.Lock()
	defer m.mu.Unlock()

	p.ID = "prov" + p.Name
	m.providers[p.ID] = p

	return p
}

func (m *mockProviderRepo) UpdateProvider(p *domain.LLMProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.providers[p.ID]; !ok {
		return domain.ErrNotFound
	}

	m.providers[p.ID] = p

	return nil
}

func (m *mockProviderRepo) DeleteProvider(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.providers[id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.providers, id)

	return nil
}

func (m *mockProviderRepo) ListProviders(projectID string) []*domain.LLMProvider {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*domain.LLMProvider, 0)

	for _, p := range m.providers {
		if p.ProjectID == "" || p.ProjectID == projectID {
			out = append(out, p)
		}
	}

	return out
}

func (m *mockProviderRepo) GetProvider(id string) (*domain.LLMProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.providers[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return p, nil
}

type mockLogRepo struct {
	mu   sync.Mutex
	logs []*domain.RequestLog
}

func newMockLogRepo() *mockLogRepo {
	return &mockLogRepo{}
}

func (m *mockLogRepo) Add(l *domain.RequestLog) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logs = append(m.logs, l)
}

func (m *mockLogRepo) ListLogs(projectID string, limit int) []*domain.RequestLog {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*domain.RequestLog, 0)
	for i := len(m.logs) - 1; i >= 0 && len(out) < limit; i-- {
		if m.logs[i].ProjectID == projectID {
			out = append(out, m.logs[i])
		}
	}

	return out
}

// ---------- Mock engine ----------.

type mockContractEngine struct {
	validateResult  usecase.ValidationResult
	parseErr        error
	endpoints       []usecase.Endpoint
	endpointsErr    error
	pathTemplate    string
	summary         string
	found           bool
	schemaJSON      string
	schemaErr       error
	exampleBody     []byte
	exampleCT       string
	exampleErr      error
	validateBodyErr error
	diffLines       []usecase.DiffLine
}

func (m *mockContractEngine) Validate(raw []byte) usecase.ValidationResult {
	return m.validateResult
}

func (m *mockContractEngine) ParseAndValidate(raw []byte) error {
	return m.parseErr
}

func (m *mockContractEngine) ListEndpoints(raw []byte) ([]usecase.Endpoint, error) {
	return m.endpoints, m.endpointsErr
}

func (m *mockContractEngine) FindOperation(raw []byte, method, path string) (string, string, bool) {
	return m.pathTemplate, m.summary, m.found
}

func (m *mockContractEngine) ResponseSchemaJSON(raw []byte, method, path, statusCode string) (string, error) {
	return m.schemaJSON, m.schemaErr
}

func (m *mockContractEngine) ExampleResponse(raw []byte, method, path, statusCode string) ([]byte, string, error) {
	return m.exampleBody, m.exampleCT, m.exampleErr
}

func (m *mockContractEngine) ValidateResponseBody(raw []byte, method, path, statusCode string, body []byte) error {
	return m.validateBodyErr
}

func (m *mockContractEngine) Diff(a, b string) []usecase.DiffLine {
	return m.diffLines
}

// ---------- Mock LLM gateway ----------.

type mockLLMGateway struct {
	mu       sync.Mutex
	response string
	err      error
	requests []usecase.ChatRequest
}

func (m *mockLLMGateway) Complete(ctx context.Context, cfg *domain.LLMProvider, req usecase.ChatRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests = append(m.requests, req)

	return m.response, m.err
}

// ---------- Mock code generator ----------.

type mockCodeGenerator struct {
	serverZip []byte
	clientZip []byte
	err       error
}

func (m *mockCodeGenerator) GenerateServerZip(raw []byte, pkgName string) ([]byte, error) {
	return m.serverZip, m.err
}

func (m *mockCodeGenerator) GenerateClientZip(raw []byte, pkgName string) ([]byte, error) {
	return m.clientZip, m.err
}

// ---------- Mock GraphQL engine ----------.

type mockGraphQLEngine struct {
	validateResult   usecase.GraphQLValidationResult
	parseErr         error
	introspection    string
	introspectionErr error
	operations       []usecase.GraphQLOperation
	operationsErr    error
	executeBody      []byte
	executeErr       error
}

func (m *mockGraphQLEngine) Validate(raw []byte) usecase.GraphQLValidationResult {
	return m.validateResult
}

func (m *mockGraphQLEngine) ParseAndValidate(raw []byte) error {
	return m.parseErr
}

func (m *mockGraphQLEngine) Introspection(raw []byte) (string, error) {
	return m.introspection, m.introspectionErr
}

func (m *mockGraphQLEngine) ListOperations(raw []byte) ([]usecase.GraphQLOperation, error) {
	return m.operations, m.operationsErr
}

func (m *mockGraphQLEngine) Execute(raw []byte, query, operationName string, variables map[string]any) ([]byte, error) {
	return m.executeBody, m.executeErr
}

// errSentinel is a generic error for tests.
var errSentinel = errors.New("sentinel error")
