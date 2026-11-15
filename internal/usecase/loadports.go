package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// LoadParam is a parameter of a contract operation with ready-to-use values.
type LoadParam struct {
	Name     string
	Required bool
	// Values are valid example values taken from the schema (its example,
	// enum, or a generated one); a request picks one at random.
	Values []string
}

// LoadOperation is a contract operation as the load tester needs it: where to
// send a request and what valid parameters and bodies look like.
type LoadOperation struct {
	// ID is the operationId, empty when the contract has none.
	ID     string
	Method string
	// Path is the template, for example /pets/{petId}.
	Path         string
	PathParams   []LoadParam
	QueryParams  []LoadParam
	HeaderParams []LoadParam
	// BodyContentType is the request media type, empty when there is no body.
	BodyContentType string
	// Bodies are valid example request bodies (JSON); a request picks one at
	// random so load is not one identical payload.
	Bodies [][]byte
	// Safe is true for GET and HEAD, which a generated plan may call freely.
	Safe bool
}

// Ref is the operation as written in a plan: "GET /pets/{petId}".
func (o *LoadOperation) Ref() string { return fmt.Sprintf("%s %s", o.Method, o.Path) }

// Name is how the operation is called in reports: the operationId, or Ref.
func (o *LoadOperation) Name() string {
	if o.ID != "" {
		return o.ID
	}

	return o.Ref()
}

// LoadOperationLister reads the operations of a contract. The OpenAPI engine
// implements it.
type LoadOperationLister interface {
	LoadOperations(raw []byte) ([]LoadOperation, error)
}

// ContractViolation is one way a response differs from its contract.
type ContractViolation struct {
	// Path locates the problem in the JSON body ("$.items[0].id"), or is
	// "status" / "content-type" / "body" for the other kinds.
	Path    string
	Message string
}

// ResponseValidator checks responses against a contract. It is built once per
// run and used from many goroutines.
type ResponseValidator interface {
	// ValidateResponse checks one response. checked is false when the
	// response was skipped (sampling) or the operation is not in the contract.
	ValidateResponse(method, path string, status int, contentType string, body []byte) (
		violations []ContractViolation, checked bool,
	)
}

// RedactHeaders returns a copy of headers with secrets (Authorization,
// cookies, API keys) replaced, the same way the request log does it.
func RedactHeaders(in map[string][]string) map[string][]string { return redactHeaders(in) }

// LoadRunStore keeps load runs and their detail files (artifacts). The file
// adapter implements it; a database adapter can replace it.
type LoadRunStore interface {
	SaveRun(ctx context.Context, run *domain.LoadRun) error
	// GetRun returns domain.ErrNotFound for an unknown run.
	GetRun(ctx context.Context, projectID, runID string) (*domain.LoadRun, error)
	// ListRuns returns the project's runs, newest first.
	ListRuns(ctx context.Context, projectID string) ([]domain.LoadRun, error)
	// DeleteRun removes a run with its artifacts.
	DeleteRun(ctx context.Context, projectID, runID string) error
	SaveArtifact(ctx context.Context, projectID, runID, name string, data []byte) error
	// ReadArtifact returns domain.ErrNotFound for an unknown artifact.
	ReadArtifact(ctx context.Context, projectID, runID, name string) ([]byte, error)
}

// LoadProgress is a live reading of a run in progress.
type LoadProgress struct {
	Elapsed  time.Duration `json:"elapsed"`
	Requests int64         `json:"requests"`
	Errors   int64         `json:"errors"`
	Dropped  int64         `json:"dropped"`
	RPS      float64       `json:"rps"`
	P95Ms    float64       `json:"p95Ms"`
	InFlight int64         `json:"inFlight"`
	VUs      int64         `json:"vus"`
}

// LoadExecution is what the engine is asked to run.
type LoadExecution struct {
	RunID     string
	ProjectID string
	// Actor is who started the run, for the report.
	Actor  string
	Plan   *domain.Plan
	Target *LoadTarget
	// Progress is called about once a second; it may be nil.
	Progress func(LoadProgress)
}

// LoadOutcome is what a run produced.
type LoadOutcome struct {
	Thresholds []domain.LoadThresholdResult
	Totals     *domain.LoadTotals
	// AbortReason is set when a threshold with abortOnFail ended the run early.
	AbortReason string
	// Artifacts are the detail files, by name (summary.json, ...).
	Artifacts map[string][]byte
}

// LoadEngine executes a plan. When ctx is cancelled it stops the load within
// the plan's graceful-stop time and still returns what was measured so far.
type LoadEngine interface {
	Execute(ctx context.Context, in *LoadExecution) (*LoadOutcome, error)
}
