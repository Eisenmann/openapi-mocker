package openapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Engine implements usecase.ContractEngine on top of kin-openapi. The contract
// is re-parsed on every call — for typical OpenAPI file sizes (dozens to
// hundreds of operations) this is negligible and greatly simplifies the code
// (no need to invalidate a cache when a new version is published). If
// profiling ever shows parsing is a bottleneck, this is a convenient place
// to add an LRU cache keyed by raw hash without changing the usecase layer.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

var _ usecase.ContractEngine = (*Engine)(nil)

func (e *Engine) Validate(raw []byte) usecase.ValidationResult {
	doc, err := parseAndValidate(raw)
	if err != nil {
		return usecase.ValidationResult{Valid: false, Errors: []string{err.Error()}}
	}
	opCount := 0
	pathCount := 0
	if doc.Paths != nil {
		pathCount = doc.Paths.Len()
		for _, item := range doc.Paths.Map() {
			opCount += len(item.Operations())
		}
	}
	return usecase.ValidationResult{Valid: true, PathCount: pathCount, OpCount: opCount}
}

func (e *Engine) ParseAndValidate(raw []byte) error {
	_, err := parseAndValidate(raw)
	return err
}

func (e *Engine) ListEndpoints(raw []byte) ([]usecase.Endpoint, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}
	var out []usecase.Endpoint
	if doc.Paths != nil {
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				out = append(out, usecase.Endpoint{Path: path, Method: strings.ToUpper(method), Summary: op.Summary})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

func (e *Engine) FindOperation(raw []byte, method, path string) (pathTemplate, summary string, found bool) {
	doc, err := parse(raw)
	if err != nil {
		return "", "", false
	}
	m := findOperation(doc, method, path)
	if m == nil {
		return "", "", false
	}
	return m.PathTemplate, m.Operation.Summary, true
}

func (e *Engine) ResponseSchemaJSON(raw []byte, method, path, statusCode string) (string, error) {
	doc, err := parse(raw)
	if err != nil {
		return "", err
	}
	m := findOperation(doc, method, path)
	if m == nil {
		return "", fmt.Errorf("operation %s %s not found in contract", method, path)
	}
	return responseSchemaJSON(m.Operation, statusCode)
}

func (e *Engine) ExampleResponse(
	raw []byte, method, path, statusCode string,
) (body []byte, contentType string, err error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, "", err
	}
	m := findOperation(doc, method, path)
	if m == nil {
		return nil, "", fmt.Errorf("operation %s %s not found in contract", method, path)
	}
	status := statusCode
	if m.Operation.Responses != nil && m.Operation.Responses.Value(status) == nil {
		for code := range m.Operation.Responses.Map() {
			status = code
			break
		}
	}
	body, contentType, _ = responseExample(m.Operation, status)
	return body, contentType, nil
}

func (e *Engine) ValidateResponseBody(raw []byte, method, path, statusCode string, body []byte) error {
	doc, err := parse(raw)
	if err != nil {
		return err
	}
	m := findOperation(doc, method, path)
	if m == nil {
		return fmt.Errorf("operation %s %s not found in contract", method, path)
	}
	return validateBodyAgainstResponseSchema(m.Operation, statusCode, body)
}

func (e *Engine) Diff(a, b string) []usecase.DiffLine {
	return diffLines(a, b)
}
