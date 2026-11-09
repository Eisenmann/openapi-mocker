package usecase

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// GraphQLMockPath is the mock-server path template for executing GraphQL
// operations. It is shared by the HTTP router and the request logger so the
// two can't drift.
const GraphQLMockPath = "/mock/%s/graphql"

// GraphQLValidationResult contains the outcome of validating a GraphQL SDL schema.
type GraphQLValidationResult struct {
	Valid          bool
	Errors         []string
	TypeCount      int
	QueryFields    int
	MutationFields int
}

// GraphQLArgument describes an operation argument.
type GraphQLArgument struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	HasDefault bool   `json:"hasDefault"`
}

// GraphQLOperation describes a root query/mutation/subscription field.
type GraphQLOperation struct {
	Type        string            `json:"type"` // query | mutation | subscription.
	Name        string            `json:"name"`
	Description string            `json:"description"`
	ReturnType  string            `json:"returnType"`
	Arguments   []GraphQLArgument `json:"arguments"`
}

// GraphQLLocation is a line/column pair inside a GraphQL document.
type GraphQLLocation struct {
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
}

// GraphQLQueryErrorItem is one error entry in a GraphQL response, per the spec.
type GraphQLQueryErrorItem struct {
	Message   string            `json:"message"`
	Locations []GraphQLLocation `json:"locations,omitempty"`
}

// GraphQLQueryError aggregates document-level errors from parsing or validating
// a GraphQL query. Per GraphQL-over-HTTP, a non-nil GraphQLQueryError is
// returned with HTTP 200 and an "errors" array in the body.
type GraphQLQueryError struct {
	Errors []GraphQLQueryErrorItem `json:"errors"`
}

// Error implements the error interface. It joins all messages so the error can
// be serialized as a plain string when structured formatting is not available.
func (e *GraphQLQueryError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, ge := range e.Errors {
		parts[i] = ge.Message
	}

	return strings.Join(parts, "; ")
}

// ErrNotGraphQLContract is returned when a project's active contract is not a
// GraphQL SDL document.
var ErrNotGraphQLContract = errors.New("project is not a GraphQL contract")

// GraphQLEngine is the port that the usecase layer depends on for all GraphQL
// work. The adapter (implemented with gqlparser) fulfills it. No gqlparser
// type crosses this boundary.
//
// Execute returns an error of type *GraphQLQueryError for document-level
// errors (parse/validation) — those are valid GraphQL-over-HTTP 200 responses.
// Any other error is an infrastructure/server-side failure.
type GraphQLEngine interface {
	Validate(raw []byte) GraphQLValidationResult
	ParseAndValidate(raw []byte) error
	Introspection(raw []byte) (string, error)
	ListOperations(raw []byte) ([]GraphQLOperation, error)
	Execute(raw []byte, query, operationName string, variables map[string]any) ([]byte, error)
}

// GraphQLOperationTyper is an optional capability of a GraphQLEngine: it
// parses a query document and returns the type ("query", "mutation" or
// "subscription") of the selected operation.
type GraphQLOperationTyper interface {
	OperationType(query, operationName string) (string, error)
}

// GraphQLServingService serves mock responses for GraphQL contracts. Like
// MockServingService for OpenAPI, it decides "what to respond" (per the
// schema + query), not "how to write it to the socket".
type GraphQLServingService struct {
	contracts ContractRepository
	logs      LogRepository
	engine    GraphQLEngine
}

func NewGraphQLServingService(
	contracts ContractRepository,
	logs LogRepository,
	engine GraphQLEngine,
) *GraphQLServingService {
	return &GraphQLServingService{contracts: contracts, logs: logs, engine: engine}
}

// Serve handles a GraphQL operation and returns the mock JSON body.
// An error of type *GraphQLQueryError is a document-level failure that should
// be returned with HTTP 200 and an "errors" array per GraphQL-over-HTTP; all
// other errors are server/infrastructure failures.
func (s *GraphQLServingService) Serve(
	projectID, query, operationName string,
	variables map[string]any,
) ([]byte, error) {
	start := time.Now()
	call := &gqlCall{start: start, request: graphQLRequestBody(query, operationName, variables), response: nil}

	raw, err := s.getGraphQL(projectID)
	if err != nil {
		code := StatusFromError(err)
		call.response = errorBody(err.Error())
		s.logResult(projectID, code, false, call)

		return nil, err
	}

	body, err := s.engine.Execute([]byte(raw), query, operationName, variables)
	if err != nil {
		var qe *GraphQLQueryError
		if errors.As(err, &qe) {
			call.response = graphQLErrorsBody(qe)
			s.logResult(projectID, StatusOK, false, call)
			// Execute always returns a nil body alongside *GraphQLQueryError;
			// the handler serializes qe.Errors itself.
			return nil, err
		}

		call.response = errorBody(err.Error())
		s.logResult(projectID, StatusInternalServerError, false, call)

		return nil, err
	}

	call.response = body
	s.logResult(projectID, StatusOK, true, call)

	return body, nil
}

// IsSubscription reports whether the operation selected in the document is a
// subscription. A document that does not parse is not one: executing it
// reports the real error.
func (s *GraphQLServingService) IsSubscription(query, operationName string) bool {
	typer, ok := s.engine.(GraphQLOperationTyper)
	if !ok {
		return false
	}

	kind, err := typer.OperationType(query, operationName)

	return err == nil && kind == "subscription"
}

// Schema returns the introspected SDL of the project's GraphQL schema.
func (s *GraphQLServingService) Schema(projectID string) (string, error) {
	raw, err := s.getGraphQL(projectID)
	if err != nil {
		return "", err
	}

	return s.engine.Introspection([]byte(raw))
}

// Operations lists the root query/mutation/subscription fields of the schema.
func (s *GraphQLServingService) Operations(projectID string) ([]GraphQLOperation, error) {
	raw, err := s.getGraphQL(projectID)
	if err != nil {
		return nil, err
	}

	return s.engine.ListOperations([]byte(raw))
}

func (s *GraphQLServingService) getGraphQL(projectID string) (string, error) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return "", err
	}

	if c.Format != FormatGraphQL {
		return "", fmt.Errorf("%w: %s", ErrNotGraphQLContract, projectID)
	}

	return c.Raw, nil
}

// gqlCall is what logResult needs to know about a served GraphQL operation.
type gqlCall struct {
	start    time.Time
	request  []byte
	response []byte
}

// graphQLRequestBody rebuilds the JSON body of the operation for the log.
func graphQLRequestBody(query, operationName string, variables map[string]any) []byte {
	b, err := json.Marshal(graphQLRequest{Query: query, OperationName: operationName, Variables: variables})
	if err != nil {
		return []byte(query)
	}

	return b
}

// graphQLRequest is the JSON body of a GraphQL-over-HTTP request.
type graphQLRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// graphQLErrorsBody renders the {"errors": [...]} body of a failed document.
func graphQLErrorsBody(qe *GraphQLQueryError) []byte {
	b, err := json.Marshal(map[string]any{"errors": qe.Errors})
	if err != nil {
		return nil
	}

	return b
}

// logResult records a request log entry. The duration is the wall-clock time
// spent serving the GraphQL operation.
func (s *GraphQLServingService) logResult(projectID string, statusCode int, matched bool, call *gqlCall) {
	endpoint := ""
	if matched {
		endpoint = graphQLEndpoint
	}

	s.logs.Add(&domain.RequestLog{
		ID:          "",
		ProjectID:   projectID,
		Method:      methodPost,
		Path:        fmt.Sprintf(GraphQLMockPath, projectID),
		Query:       "",
		StatusCode:  statusCode,
		MatchedRule: "",
		Matched:     matched,
		Timestamp:   time.Now().UTC(),
		DurationMs:  time.Since(call.start).Milliseconds(),
		Violations:  nil, Fault: "", Source: "",
		Endpoint: endpoint, Captured: false,
		Request:  rawCapture(nil, call.request),
		Response: rawCapture(jsonHeader(), call.response),
	})
}
