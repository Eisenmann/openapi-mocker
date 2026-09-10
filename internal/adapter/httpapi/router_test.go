package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// ---------- In-memory mock repositories ----------.

type memProjectRepo struct {
	mu       sync.Mutex
	projects map[string]*domain.Project
	order    []string
}

func newMemProjectRepo() *memProjectRepo {
	return &memProjectRepo{projects: map[string]*domain.Project{}}
}

func (m *memProjectRepo) Create(name, description string) *domain.Project {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := "p" + strings.ReplaceAll(name, " ", "")
	p := &domain.Project{ID: id, Name: name, Description: description}
	m.projects[p.ID] = p
	m.order = append(m.order, p.ID)

	return p
}

func (m *memProjectRepo) List() []*domain.Project {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*domain.Project, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.projects[id])
	}

	return out
}

func (m *memProjectRepo) Get(id string) (*domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.projects[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return p, nil
}

func (m *memProjectRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.projects[id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.projects, id)

	return nil
}

type memContractRepo struct {
	mu        sync.Mutex
	contracts map[string][]*domain.Contract
}

func newMemContractRepo() *memContractRepo {
	return &memContractRepo{contracts: map[string][]*domain.Contract{}}
}

func (m *memContractRepo) AddVersion(projectID, format, raw, source string) *domain.Contract {
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

func (m *memContractRepo) GetActive(projectID string) (*domain.Contract, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	history := m.contracts[projectID]
	if len(history) == 0 {
		return nil, domain.ErrNotFound
	}

	return history[len(history)-1], nil
}

func (m *memContractRepo) GetVersion(projectID string, version int) (*domain.Contract, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.contracts[projectID] {
		if c.Version == version {
			return c, nil
		}
	}

	return nil, domain.ErrNotFound
}

func (m *memContractRepo) ListVersions(projectID string) []*domain.Contract {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]*domain.Contract{}, m.contracts[projectID]...)
}

type memMockRepo struct {
	mu    sync.Mutex
	mocks map[string]*domain.MockRule
}

func newMemMockRepo() *memMockRepo {
	return &memMockRepo{mocks: map[string]*domain.MockRule{}}
}

func (m *memMockRepo) CreateMock(mr *domain.MockRule) *domain.MockRule {
	m.mu.Lock()
	defer m.mu.Unlock()

	mr.ID = "mock" + strings.ReplaceAll(mr.Path, "/", "_") + mr.Method + mr.Scenario
	m.mocks[mr.ID] = mr

	return mr
}

func (m *memMockRepo) UpdateMock(mr *domain.MockRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.mocks[mr.ID]; !ok {
		return domain.ErrNotFound
	}

	m.mocks[mr.ID] = mr

	return nil
}

func (m *memMockRepo) DeleteMock(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.mocks[id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.mocks, id)

	return nil
}

func (m *memMockRepo) ListMocks(projectID string) []*domain.MockRule {
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

func (m *memMockRepo) GetMock(id string) (*domain.MockRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mr, ok := m.mocks[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return mr, nil
}

type memProviderRepo struct {
	mu        sync.Mutex
	providers map[string]*domain.LLMProvider
}

func newMemProviderRepo() *memProviderRepo {
	return &memProviderRepo{providers: map[string]*domain.LLMProvider{}}
}

func (m *memProviderRepo) CreateProvider(p *domain.LLMProvider) *domain.LLMProvider {
	m.mu.Lock()
	defer m.mu.Unlock()

	p.ID = "prov" + p.Name
	m.providers[p.ID] = p

	return p
}

func (m *memProviderRepo) UpdateProvider(p *domain.LLMProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.providers[p.ID]; !ok {
		return domain.ErrNotFound
	}

	m.providers[p.ID] = p

	return nil
}

func (m *memProviderRepo) DeleteProvider(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.providers[id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.providers, id)

	return nil
}

func (m *memProviderRepo) ListProviders(projectID string) []*domain.LLMProvider {
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

func (m *memProviderRepo) GetProvider(id string) (*domain.LLMProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.providers[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return p, nil
}

type memLogRepo struct {
	mu   sync.Mutex
	logs []*domain.RequestLog
}

func newMemLogRepo() *memLogRepo {
	return &memLogRepo{}
}

func (m *memLogRepo) Add(l *domain.RequestLog) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logs = append(m.logs, l)
}

func (m *memLogRepo) ListLogs(projectID string, limit int) []*domain.RequestLog {
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

type stubEngine struct {
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

func (m *stubEngine) Validate(raw []byte) usecase.ValidationResult {
	return m.validateResult
}

func (m *stubEngine) ParseAndValidate(raw []byte) error {
	return m.parseErr
}

func (m *stubEngine) ListEndpoints(raw []byte) ([]usecase.Endpoint, error) {
	return m.endpoints, m.endpointsErr
}

func (m *stubEngine) FindOperation(raw []byte, method, path string) (string, string, bool) {
	return m.pathTemplate, m.summary, m.found
}

func (m *stubEngine) ResponseSchemaJSON(raw []byte, method, path, statusCode string) (string, error) {
	return m.schemaJSON, m.schemaErr
}

func (m *stubEngine) ExampleResponse(raw []byte, method, path, statusCode string) ([]byte, string, error) {
	return m.exampleBody, m.exampleCT, m.exampleErr
}

func (m *stubEngine) ValidateResponseBody(raw []byte, method, path, statusCode string, body []byte) error {
	return m.validateBodyErr
}

func (m *stubEngine) Diff(a, b string) []usecase.DiffLine {
	return m.diffLines
}

// ---------- Mock LLM gateway ----------.

type stubLLM struct {
	mu       sync.Mutex
	response string
	err      error
}

func (m *stubLLM) Complete(ctx context.Context, cfg *domain.LLMProvider, req usecase.ChatRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.response, m.err
}

// ---------- Mock code generator ----------.

type stubCodegen struct {
	serverZip []byte
	clientZip []byte
	err       error
}

func (m *stubCodegen) GenerateServerZip(raw []byte, pkgName string) ([]byte, error) {
	return m.serverZip, m.err
}

func (m *stubCodegen) GenerateClientZip(raw []byte, pkgName string) ([]byte, error) {
	return m.clientZip, m.err
}

// ---------- Mock GraphQL engine ----------.

type stubGraphQLEngine struct {
	validateResult   usecase.GraphQLValidationResult
	parseErr         error
	introspection    string
	introspectionErr error
	operations       []usecase.GraphQLOperation
	operationsErr    error
	executeBody      []byte
	executeErr       error
}

func (m *stubGraphQLEngine) Validate(raw []byte) usecase.GraphQLValidationResult {
	return m.validateResult
}

func (m *stubGraphQLEngine) ParseAndValidate(raw []byte) error {
	return m.parseErr
}

func (m *stubGraphQLEngine) Introspection(raw []byte) (string, error) {
	return m.introspection, m.introspectionErr
}

func (m *stubGraphQLEngine) ListOperations(raw []byte) ([]usecase.GraphQLOperation, error) {
	return m.operations, m.operationsErr
}

func (m *stubGraphQLEngine) Execute(raw []byte, query, operationName string, variables map[string]any) ([]byte, error) {
	return m.executeBody, m.executeErr
}

// ---------- Test helpers ----------.

const validOpenAPI = `openapi: 3.0.0
info:
  title: Test API
  version: 1.0.0
paths:
  /users:
    get:
      responses:
        '200':
          description: OK
`

func newTestRouter(t *testing.T) (http.Handler, *memProjectRepo, *memContractRepo, *memMockRepo, *memProviderRepo, *memLogRepo, *stubEngine, *stubLLM, *stubCodegen, *stubGraphQLEngine) {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	mocks := newMemMockRepo()
	providers := newMemProviderRepo()
	logs := newMemLogRepo()
	engine := &stubEngine{}
	llm := &stubLLM{}
	codegen := &stubCodegen{}
	gqlEngine := &stubGraphQLEngine{}

	svc := httpapi.Services{
		Projects:       usecase.NewProjectService(projects),
		Contracts:      usecase.NewContractService(contracts, providers, engine, llm),
		Mocks:          usecase.NewMockService(mocks, contracts, providers, engine, llm),
		Providers:      usecase.NewProviderService(providers, llm),
		MockServing:    usecase.NewMockServingService(contracts, mocks, logs, engine),
		GraphQLServing: usecase.NewGraphQLServingService(contracts, logs, gqlEngine),
		Codegen:        usecase.NewCodegenService(contracts, codegen),
		Logs:           usecase.NewLogService(logs),
	}

	return httpapi.NewRouter(svc), projects, contracts, mocks, providers, logs, engine, llm, codegen, gqlEngine
}

func doRequest(t *testing.T, h http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v interface{}) {
	t.Helper()

	err := json.Unmarshal(rec.Body.Bytes(), v)
	if err != nil {
		t.Fatalf("failed to decode JSON: %v, body: %s", err, rec.Body.String())
	}
}

// ---------- Tests ----------.

func TestRouter_Healthz(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, _, _, _, _, _ := newTestRouter(t)

	rec := doRequest(t, h, "GET", "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	if rec.Body.String() != "ok" {
		t.Errorf("expected 'ok', got %q", rec.Body.String())
	}
}

func TestRouter_CORS(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, _, _, _, _, _ := newTestRouter(t)

	rec := doRequest(t, h, "OPTIONS", "/api/projects", nil, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS header")
	}
}

func TestRouter_Projects(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, _, _, _, _, _ := newTestRouter(t)

	// Create.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"My Project","description":"desc"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var p domain.Project
	decodeJSON(t, rec, &p)

	if p.Name != "My Project" {
		t.Errorf("expected name, got %q", p.Name)
	}

	// List.
	rec = doRequest(t, h, "GET", "/api/projects", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var projects []domain.Project
	decodeJSON(t, rec, &projects)

	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}

	// Get.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Get missing.
	rec = doRequest(t, h, "GET", "/api/projects/missing", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Create with empty name.
	rec = doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":""}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Invalid JSON.
	rec = doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{invalid`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Delete.
	rec = doRequest(t, h, "DELETE", "/api/projects/"+p.ID, nil, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}

	// Delete missing.
	rec = doRequest(t, h, "DELETE", "/api/projects/missing", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter_Contracts(t *testing.T) {
	t.Parallel()

	h, _, _, _, providers, _, engine, llm, _, _ := newTestRouter(t)
	engine.parseErr = nil

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Save contract.
	contractBody, _ := json.Marshal(map[string]string{"raw": validOpenAPI})

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", bytes.NewReader(contractBody), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var c domain.Contract
	decodeJSON(t, rec, &c)

	if c.Version != 1 {
		t.Errorf("expected version 1, got %d", c.Version)
	}

	// Get active.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Get active missing.
	rec = doRequest(t, h, "GET", "/api/projects/missing/contract", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// List versions.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/versions", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var versions []domain.Contract
	decodeJSON(t, rec, &versions)

	if len(versions) != 1 {
		t.Errorf("expected 1 version, got %d", len(versions))
	}

	// Get version.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/versions/1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Get version invalid.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/versions/abc", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Get version missing.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/versions/99", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Save empty contract.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", strings.NewReader(`{"raw":""}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rec.Code)
	}

	// Save invalid contract.
	engine.parseErr = errors.New("invalid contract")

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", strings.NewReader(`{"raw":"invalid"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rec.Code)
	}

	engine.parseErr = nil

	// Rollback.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract/versions/1/rollback", nil, nil)
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Rollback invalid version.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract/versions/abc/rollback", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Rollback missing version.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract/versions/99/rollback", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Diff.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/diff?from=1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Diff missing from.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/diff", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Diff invalid to.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/contract/diff?from=1&to=abc", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Validate.
	validateBody, _ := json.Marshal(map[string]string{"raw": validOpenAPI})

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract/validate", bytes.NewReader(validateBody), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// List endpoints.
	engine.endpoints = []usecase.Endpoint{{Path: "/users", Method: "GET"}}

	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/endpoints", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// List endpoints missing contract.
	rec = doRequest(t, h, "GET", "/api/projects/missing/endpoints", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Create provider for generation.
	providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai", BaseURL: "http://localhost"})

	// Generate contract.
	llm.response = `{"openapi":"3.0.0","info":{"title":"T","version":"1"},"paths":{}}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract/generate", strings.NewReader(`{"providerId":"provopenai","description":"test"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Generate contract with LLM error.
	llm.err = errors.New("llm error")

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract/generate", strings.NewReader(`{"providerId":"provopenai","description":"test"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}

	llm.err = nil

	// Multipart upload.
	var buf bytes.Buffer
	buf.WriteString("--boundary\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"contract.yaml\"\r\n")
	buf.WriteString("Content-Type: application/yaml\r\n\r\n")
	buf.WriteString(validOpenAPI)
	buf.WriteString("\r\n--boundary--\r\n")

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", &buf, map[string]string{"Content-Type": "multipart/form-data; boundary=boundary"})
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Multipart without file.
	buf.Reset()
	buf.WriteString("--boundary\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"other\"\r\n\r\n")
	buf.WriteString("value")
	buf.WriteString("\r\n--boundary--\r\n")

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", &buf, map[string]string{"Content-Type": "multipart/form-data; boundary=boundary"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestRouter_Mocks(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, _, _, _, _, _ := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Create mock.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/mocks", strings.NewReader(`{"path":"/users","method":"GET","statusCode":200,"body":"{\"ok\":true}"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var m domain.MockRule
	decodeJSON(t, rec, &m)

	if m.ID == "" {
		t.Fatal("expected mock ID")
	}

	// List mocks.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/mocks", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var mocks []domain.MockRule
	decodeJSON(t, rec, &mocks)

	if len(mocks) != 1 {
		t.Errorf("expected 1 mock, got %d", len(mocks))
	}

	// Create mock with invalid body.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/mocks", strings.NewReader(`{"path":"/x","method":"GET","body":"not-json"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Update mock.
	rec = doRequest(t, h, "PUT", "/api/mocks/"+m.ID, strings.NewReader(`{"path":"/users","method":"GET","statusCode":201,"body":"{\"ok\":true}"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Update missing mock.
	rec = doRequest(t, h, "PUT", "/api/mocks/missing", strings.NewReader(`{"path":"/x","method":"GET"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Delete mock.
	rec = doRequest(t, h, "DELETE", "/api/mocks/"+m.ID, nil, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}

	// Delete missing mock.
	rec = doRequest(t, h, "DELETE", "/api/mocks/missing", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter_Providers(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, _, _, llm, _, _ := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Create provider.
	rec = doRequest(t, h, "POST", "/api/llm-providers", strings.NewReader(`{"name":"openai","type":"openai","baseUrl":"http://localhost"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var prov domain.LLMProvider
	decodeJSON(t, rec, &prov)

	if prov.ID == "" {
		t.Fatal("expected provider ID")
	}

	// List global.
	rec = doRequest(t, h, "GET", "/api/llm-providers", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var providers []domain.LLMProvider
	decodeJSON(t, rec, &providers)

	if len(providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(providers))
	}

	// List for project.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/llm-providers", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Create with missing name/type.
	rec = doRequest(t, h, "POST", "/api/llm-providers", strings.NewReader(`{"name":""}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Update.
	rec = doRequest(t, h, "PUT", "/api/llm-providers/"+prov.ID, strings.NewReader(`{"name":"renamed","type":"openai"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Update missing.
	rec = doRequest(t, h, "PUT", "/api/llm-providers/missing", strings.NewReader(`{"name":"x","type":"y"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Test provider.
	llm.response = "ok"

	rec = doRequest(t, h, "POST", "/api/llm-providers/"+prov.ID+"/test", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Test provider error.
	llm.err = errors.New("llm error")

	rec = doRequest(t, h, "POST", "/api/llm-providers/"+prov.ID+"/test", nil, nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}

	llm.err = nil

	// Delete.
	rec = doRequest(t, h, "DELETE", "/api/llm-providers/"+prov.ID, nil, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}

	// Delete missing.
	rec = doRequest(t, h, "DELETE", "/api/llm-providers/missing", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter_MockServing(t *testing.T) {
	t.Parallel()

	h, _, contracts, mocks, _, _, engine, _, _, _ := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add contract.
	contracts.AddVersion(p.ID, "yaml", validOpenAPI, "manual")

	// Add mock rule.
	engine.pathTemplate = "/users"
	engine.found = true

	mocks.CreateMock(&domain.MockRule{
		ProjectID: p.ID, Path: "/users", Method: "GET",
		StatusCode: 200, Body: `{"ok":true}`,
	})

	// Serve mock.
	rec = doRequest(t, h, "GET", "/mock/"+p.ID+"/users", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("expected body, got %q", rec.Body.String())
	}

	if rec.Header().Get("X-Mock-Source") != "rule" {
		t.Errorf("expected X-Mock-Source header, got %q", rec.Header().Get("X-Mock-Source"))
	}

	// Serve mock root.
	rec = doRequest(t, h, "GET", "/mock/"+p.ID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Serve mock with scenario header.
	rec = doRequest(t, h, "GET", "/mock/"+p.ID+"/users", nil, map[string]string{"X-Mock-Scenario": "error"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Serve mock for missing project.
	rec = doRequest(t, h, "GET", "/mock/missing/users", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// Serve mock for missing endpoint.
	engine.found = false

	rec = doRequest(t, h, "GET", "/mock/"+p.ID+"/nonexistent", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	engine.found = true
}

func TestRouter_Codegen(t *testing.T) {
	t.Parallel()

	h, _, contracts, _, _, _, _, _, codegen, _ := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add contract.
	contracts.AddVersion(p.ID, "yaml", validOpenAPI, "manual")

	// Codegen server.
	codegen.serverZip = []byte("zipdata")

	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/codegen/server", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("expected zip content type, got %q", rec.Header().Get("Content-Type"))
	}

	// Codegen client.
	codegen.clientZip = []byte("zipdata")

	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/codegen/client", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Codegen missing contract.
	rec = doRequest(t, h, "GET", "/api/projects/missing/codegen/server", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter_Logs(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, logs, _, _, _, _ := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add a log.
	logs.Add(&domain.RequestLog{ProjectID: p.ID, Method: "GET", Path: "/users", StatusCode: 200})

	// List logs.
	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/logs", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var logEntries []domain.RequestLog
	decodeJSON(t, rec, &logEntries)

	if len(logEntries) != 1 {
		t.Errorf("expected 1 log, got %d", len(logEntries))
	}
}

func TestRouter_GraphQL(t *testing.T) {
	t.Parallel()

	h, _, contracts, _, _, _, _, _, _, gqlEngine := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add GraphQL contract.
	contracts.AddVersion(p.ID, "graphql", "type Query { ping: String }", "manual")

	// Serve GraphQL.
	gqlEngine.executeBody = []byte(`{"data":{"ping":"pong"}}`)

	rec = doRequest(t, h, "POST", "/mock/"+p.ID+"/graphql", strings.NewReader(`{"query":"{ ping }"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if rec.Body.String() != `{"data":{"ping":"pong"}}` {
		t.Errorf("expected body, got %q", rec.Body.String())
	}

	// GraphQL query error.
	gqlEngine.executeErr = &usecase.GraphQLQueryError{Errors: []usecase.GraphQLQueryErrorItem{{Message: "parse error"}}}

	rec = doRequest(t, h, "POST", "/mock/"+p.ID+"/graphql", strings.NewReader(`{"query":"{ bad"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for GraphQL query error, got %d", rec.Code)
	}

	gqlEngine.executeErr = nil

	// GraphQL server error.
	gqlEngine.executeErr = errors.New("server error")

	rec = doRequest(t, h, "POST", "/mock/"+p.ID+"/graphql", strings.NewReader(`{"query":"{ ping }"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}

	gqlEngine.executeErr = nil

	// Empty query.
	rec = doRequest(t, h, "POST", "/mock/"+p.ID+"/graphql", strings.NewReader(`{"query":""}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// Invalid JSON.
	rec = doRequest(t, h, "POST", "/mock/"+p.ID+"/graphql", strings.NewReader(`{invalid`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	// GraphQL schema.
	gqlEngine.introspection = "type Query { ping: String }"

	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/graphql/schema", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// GraphQL operations.
	gqlEngine.operations = []usecase.GraphQLOperation{{Type: "query", Name: "ping"}}

	rec = doRequest(t, h, "GET", "/api/projects/"+p.ID+"/graphql/operations", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// GraphQL for non-GraphQL contract.
	contracts.AddVersion(p.ID, "yaml", validOpenAPI, "manual")

	rec = doRequest(t, h, "POST", "/mock/"+p.ID+"/graphql", strings.NewReader(`{"query":"{ ping }"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestRouter_SPAFallback(t *testing.T) {
	t.Parallel()

	h, _, _, _, _, _, _, _, _, _ := newTestRouter(t)

	// SPA fallback for non-API path.
	rec := doRequest(t, h, "GET", "/", nil, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Errorf("expected 200 or 404, got %d", rec.Code)
	}

	// API path should 404.
	rec = doRequest(t, h, "GET", "/api/nonexistent", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}
