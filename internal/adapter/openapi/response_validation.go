package openapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// pathBody is the path of a violation in the response body.
const pathBody = "body"

// ResponseValidator checks responses against a contract during a load test.
// The contract is parsed once; ValidateResponse is safe for concurrent use.
type ResponseValidator struct {
	doc *openapi3.T
	// every validates one response in every N (1 = all). Validation costs far
	// more than a request, so a heavy run samples.
	every uint64
	calls atomic.Uint64
}

var _ usecase.ResponseValidator = (*ResponseValidator)(nil)

// NewResponseValidator parses the contract for response checking. sampleEvery
// of 0 or 1 validates every response, N validates one in N.
func (e *Engine) NewResponseValidator(raw []byte, sampleEvery int) (*ResponseValidator, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}

	v := new(ResponseValidator)
	v.doc = doc
	v.every = 1

	if sampleEvery > 1 {
		v.every = uint64(sampleEvery) //nolint:gosec // positive, checked above
	}

	return v, nil
}

// ValidateResponse implements usecase.ResponseValidator. It reports a
// violation for a status the contract does not declare, for a Content-Type it
// does not list and for a body that does not match the schema. checked is
// false for sampled-out responses and operations missing from the contract.
func (v *ResponseValidator) ValidateResponse(
	method, path string, status int, contentType string, body []byte,
) (violations []usecase.ContractViolation, checked bool) {
	if v.every > 1 && v.calls.Add(1)%v.every != 0 {
		return nil, false
	}

	match := findOperation(v.doc, method, path)
	if match == nil {
		return nil, false
	}

	resp := declaredResponse(match.Operation, status)
	if resp == nil {
		return []usecase.ContractViolation{{
			Path:    "status",
			Message: fmt.Sprintf("status %d is not declared for %s %s", status, strings.ToUpper(method), match.PathTemplate),
		}}, true
	}

	if len(body) == 0 && declaresBody(resp) && statusHasBody(status, method) {
		return []usecase.ContractViolation{{
			Path: pathBody, Message: "response body is empty but the contract declares one",
		}}, true
	}

	input, err := v.input(match, method, path, status, contentType, body)
	if err != nil {
		return []usecase.ContractViolation{{Path: pathBody, Message: "response cannot be validated: " + err.Error()}}, true
	}

	return responseViolations(openapi3filter.ValidateResponse(context.Background(), input)), true
}

// input assembles the kin-openapi validation input of one response.
func (v *ResponseValidator) input(
	match *matchedOperation, method, path string, status int, contentType string, body []byte,
) (*openapi3filter.ResponseValidationInput, error) {
	req, err := http.NewRequestWithContext(context.Background(), strings.ToUpper(method), mockBaseURL+path, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}

	return &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams(match.PathTemplate, path),
			Route: &routers.Route{
				Spec:      v.doc,
				Path:      match.PathTemplate,
				PathItem:  v.doc.Paths.Find(match.PathTemplate),
				Method:    strings.ToUpper(method),
				Operation: match.Operation,
			},
		},
		Status: status,
		Header: header,
		Body:   io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{
			MultiError:         true,
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
	}, nil
}

// declaredResponse finds the response the contract declares for a status: the
// exact code, then its class ("2XX"), then "default".
func declaredResponse(op *openapi3.Operation, status int) *openapi3.ResponseRef {
	if op == nil || op.Responses == nil {
		return nil
	}

	code := strconv.Itoa(status)

	if r := op.Responses.Value(code); r != nil {
		return r
	}

	if r := op.Responses.Value(code[:1] + "XX"); r != nil {
		return r
	}

	return op.Responses.Default()
}

func declaresBody(ref *openapi3.ResponseRef) bool {
	if ref == nil || ref.Value == nil {
		return false
	}

	for _, mt := range ref.Value.Content {
		if mt != nil && mt.Schema != nil {
			return true
		}
	}

	return false
}

// statusHasBody is false for answers that never carry a body.
func statusHasBody(status int, method string) bool {
	if status == http.StatusNoContent || status == http.StatusNotModified {
		return false
	}

	return !strings.EqualFold(method, http.MethodHead)
}

// responseViolations flattens the (multi-)error of response validation.
func responseViolations(err error) []usecase.ContractViolation {
	if err == nil {
		return nil
	}

	switch e := err.(type) { //nolint:errorlint // a ResponseError wraps the schema errors it describes.
	case openapi3.MultiError:
		out := make([]usecase.ContractViolation, 0, len(e))

		for _, inner := range e {
			out = append(out, responseViolations(inner)...)
		}

		return out
	case *openapi3filter.ResponseError:
		return describeResponseError(e)
	default:
		return []usecase.ContractViolation{{Path: pathBody, Message: firstLine(err.Error())}}
	}
}

func describeResponseError(re *openapi3filter.ResponseError) []usecase.ContractViolation {
	if re.Err == nil {
		msg := nonEmptyOr(re.Reason, firstLine(re.Error()))

		return []usecase.ContractViolation{{Path: responseErrorPath(re.Reason), Message: msg}}
	}

	return describeResponseCause(re.Err, re.Reason)
}

// responseErrorPath tells a header problem from a body problem.
func responseErrorPath(reason string) string {
	if strings.Contains(reason, "Content-Type") {
		return "content-type"
	}

	return pathBody
}

func describeResponseCause(cause error, reason string) []usecase.ContractViolation {
	switch c := cause.(type) { //nolint:errorlint // schema errors may be nested in a multi-error.
	case openapi3.MultiError:
		out := make([]usecase.ContractViolation, 0, len(c))

		for _, inner := range c {
			out = append(out, describeResponseCause(inner, reason)...)
		}

		return out
	case *openapi3.SchemaError:
		msg := nonEmptyOr(c.Reason, firstLine(c.Error()))

		return []usecase.ContractViolation{{Path: jsonPathOfPointer(c.JSONPointer()), Message: msg}}
	default:
		return []usecase.ContractViolation{{
			Path:    responseErrorPath(reason),
			Message: nonEmptyOr(firstLine(cause.Error()), reason),
		}}
	}
}

// jsonPathOfPointer renders a JSON pointer's segments as "$.items[0].id".
func jsonPathOfPointer(parts []string) string {
	var b strings.Builder

	b.WriteString("$")

	for _, p := range parts {
		if _, err := strconv.Atoi(p); err == nil {
			b.WriteString("[" + p + "]")

			continue
		}

		b.WriteString("." + p)
	}

	return b.String()
}
