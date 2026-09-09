// Package usecase contains the application's business rules (Use Cases in
// Clean Architecture terms) and ports — interfaces that the usecase layer
// describes as WHAT it needs from the outside world, without knowing HOW it
// is implemented. It imports only internal/domain and the standard library —
// never net/http, kin-openapi, or file storage directly. Concrete
// implementations of the ports (JSON store, kin-openapi, HTTP clients for LLMs)
// live in internal/adapter/* and are wired into the usecase layer in
// cmd/server/main.go (the composition root) — this is dependency inversion.
package usecase

import (
	"context"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// ---------- Repositories (persistence ports) ----------.

type ProjectRepository interface {
	Create(name, description string) *domain.Project
	List() []*domain.Project
	Get(id string) (*domain.Project, error)
	// Delete removes the project and cascades deletion of all its contracts/mocks.
	// This is a referential-integrity concern of the store, not a usecase business rule.
	Delete(id string) error
}

type ContractRepository interface {
	AddVersion(projectID, format, raw, source string) *domain.Contract
	GetActive(projectID string) (*domain.Contract, error)
	GetVersion(projectID string, version int) (*domain.Contract, error)
	ListVersions(projectID string) []*domain.Contract
}

type MockRepository interface {
	CreateMock(m *domain.MockRule) *domain.MockRule
	UpdateMock(m *domain.MockRule) error
	DeleteMock(id string) error
	ListMocks(projectID string) []*domain.MockRule
	GetMock(id string) (*domain.MockRule, error)
}

type ProviderRepository interface {
	CreateProvider(p *domain.LLMProvider) *domain.LLMProvider
	UpdateProvider(p *domain.LLMProvider) error
	DeleteProvider(id string) error
	ListProviders(projectID string) []*domain.LLMProvider
	GetProvider(id string) (*domain.LLMProvider, error)
}

type LogRepository interface {
	Add(l *domain.RequestLog)
	ListLogs(projectID string, limit int) []*domain.RequestLog
}

// ---------- Contract engine (port over a specific OpenAPI library) ----------.

// ValidationResult is the contract validation result, independent of which
// parsing library the adapter uses.
type ValidationResult struct {
	Valid     bool     `json:"valid"`
	Errors    []string `json:"errors,omitempty"`
	PathCount int      `json:"pathCount"`
	OpCount   int      `json:"operationCount"`
}

// Endpoint is a brief description of a contract operation.
type Endpoint struct {
	Path    string `json:"path"`
	Method  string `json:"method"`
	Summary string `json:"summary"`
}

// DiffLineType/DiffLine — a line-by-line diff of two contract versions.
type DiffLineType string

const (
	DiffSame    DiffLineType = "same"
	DiffAdded   DiffLineType = "added"
	DiffRemoved DiffLineType = "removed"
)

type DiffLine struct {
	Type DiffLineType `json:"type"`
	Text string       `json:"text"`
}

// MockResponse is the result that the usecase layer asks the HTTP adapter to
// form: WHAT response to return (including artificial delay), but NOT HOW to
// write it to http.ResponseWriter — that is the interface-adapter's job.
type MockResponse struct {
	StatusCode  int
	ContentType string
	Headers     map[string]string
	Body        []byte
	DelayMs     int
	Source      string // "rule" | "schema-example" | "chaos-injection".
	MatchedRule string
	Matched     bool
}

// ContractEngine is the port over the OpenAPI parsing/validation library.
// All of its methods operate on usecase-layer primitive types only — no
// kin-openapi type ever crosses this boundary.
type ContractEngine interface {
	Validate(raw []byte) ValidationResult
	ParseAndValidate(raw []byte) error
	ListEndpoints(raw []byte) ([]Endpoint, error)
	// FindOperation returns the path template of the operation (e.g. /users/{id})
	// and its summary, if an operation with the given method+path exists.
	FindOperation(raw []byte, method, path string) (pathTemplate, summary string, found bool)
	ResponseSchemaJSON(raw []byte, method, path, statusCode string) (string, error)
	ExampleResponse(raw []byte, method, path, statusCode string) (body []byte, contentType string, err error)
	ValidateResponseBody(raw []byte, method, path, statusCode string, body []byte) error
	Diff(a, b string) []DiffLine
}

// ---------- LLM gateway (port over specific HTTP LLM APIs) ----------.

type ChatRequest struct {
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
	Temperature  float64
}

// LLMGateway is the single entry point for talking to any LLM provider. The
// implementation itself decides which HTTP client to use based on
// domain.LLMProvider.Type.
type LLMGateway interface {
	Complete(ctx context.Context, cfg *domain.LLMProvider, req ChatRequest) (string, error)
}

// ---------- Code generator (port over server/client code generation) ----------.

type CodeGenerator interface {
	GenerateServerZip(raw []byte, pkgName string) ([]byte, error)
	GenerateClientZip(raw []byte, pkgName string) ([]byte, error)
}
