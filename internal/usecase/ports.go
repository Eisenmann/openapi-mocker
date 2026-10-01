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
	// SetValidationMode updates how incoming mock requests are validated.
	SetValidationMode(id string, mode domain.ValidationMode) (*domain.Project, error)
	// SetStateMode updates whether (and where) the project keeps mock state.
	SetStateMode(id string, mode domain.StateMode) (*domain.Project, error)
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
	Source      string // "rule" | "schema-example" | "chaos-injection" | "request-validation".
	MatchedRule string
	Matched     bool
	// Violations holds the request-validation problems found (warn/enforce).
	Violations []string
}

// RequestData is the part of an incoming mock request the contract engine
// needs in order to validate it against the contract.
type RequestData struct {
	Method string
	Path   string
	Query  map[string][]string
	Header map[string][]string
	Body   []byte
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
	// ValidateRequest checks path/query/header parameters, content type and
	// body of req against the matching operation. It returns one message per
	// violation (nil when the request is valid or the operation is unknown).
	ValidateRequest(raw []byte, req *RequestData) []string
	// SuccessStatus returns the lowest 2xx status code the operation declares
	// (ok is false when it declares none or the operation is unknown).
	SuccessStatus(raw []byte, method, path string) (code int, ok bool)
	Diff(a, b string) []DiffLine
}

// ---------- Mock state (port over collection storage) ----------.

// StateStore holds the collections of stateful mocks. Memory and persisted
// collections live in separate namespaces, so switching a project's mode never
// mixes the two.
type StateStore interface {
	// Transact runs fn with exclusive access to the collection (created on
	// first use). The changes are kept only when fn returns nil, and are
	// written to disk for the persisted mode.
	Transact(projectID string, mode domain.StateMode, collection string, fn func(c *domain.StateCollection) error) error
	// View returns a copy of the collection (empty when it does not exist).
	View(projectID string, mode domain.StateMode, collection string) domain.StateCollection
	// Snapshot returns a copy of all collections of the project, by name.
	Snapshot(projectID string, mode domain.StateMode) map[string]domain.StateCollection
	// Replace sets the collection to c, creating it when needed.
	Replace(projectID string, mode domain.StateMode, collection string, c *domain.StateCollection) error
	// Reset removes one collection, or every collection of the project (in
	// both modes) when collection is empty.
	Reset(projectID, collection string) error
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
