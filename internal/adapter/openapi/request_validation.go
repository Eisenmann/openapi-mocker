package openapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// mockBaseURL is a placeholder origin: validation only looks at the path,
// query, headers and body of the request, never at its host.
const mockBaseURL = "http://mock"

// ValidateRequest checks req against the contract operation it addresses:
// path, query and header parameters, the request Content-Type and the body.
// Security requirements are not checked (a mock has no credentials to
// verify). It returns one message per violation, or nil when the request is
// valid, the operation is unknown or the contract cannot be parsed - those
// cases are reported by the serving path, not as request violations.
func (e *Engine) ValidateRequest(raw []byte, req *usecase.RequestData) []string {
	doc, err := parse(raw)
	if err != nil {
		return nil
	}

	match := findOperation(doc, req.Method, req.Path)
	if match == nil {
		return nil
	}

	httpReq, err := buildHTTPRequest(req)
	if err != nil {
		return []string{"request cannot be validated: " + err.Error()}
	}

	input := &openapi3filter.RequestValidationInput{
		Request:    httpReq,
		PathParams: pathParams(match.PathTemplate, req.Path),
		Route: &routers.Route{
			Spec:      doc,
			Path:      match.PathTemplate,
			PathItem:  doc.Paths.Find(match.PathTemplate),
			Method:    strings.ToUpper(req.Method),
			Operation: match.Operation,
		},
		Options: &openapi3filter.Options{
			MultiError:         true,
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
	}

	return requestViolations(openapi3filter.ValidateRequest(context.Background(), input))
}

// buildHTTPRequest assembles the net/http request kin-openapi validates.
func buildHTTPRequest(req *usecase.RequestData) (*http.Request, error) {
	target := mockBaseURL + req.Path
	if len(req.Query) > 0 {
		target += "?" + url.Values(req.Query).Encode()
	}

	httpReq, err := http.NewRequestWithContext(
		context.Background(), strings.ToUpper(req.Method), target, bytes.NewReader(req.Body),
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	for name, values := range req.Header {
		for _, v := range values {
			httpReq.Header.Add(name, v)
		}
	}

	return httpReq, nil
}

// pathParams extracts the values of the {param} segments of tmpl from the
// concrete request path (tmpl is known to match path).
func pathParams(tmpl, path string) map[string]string {
	params := map[string]string{}
	actual := splitPath(path)

	for i, seg := range splitPath(tmpl) {
		if !isParam(seg) || i >= len(actual) {
			continue
		}

		value, err := url.PathUnescape(actual[i])
		if err != nil {
			value = actual[i]
		}

		params[strings.Trim(seg, "{}")] = value
	}

	return params
}

// requestViolations flattens the (multi-)error of request validation into one
// readable message per violation.
func requestViolations(err error) []string {
	if err == nil {
		return nil
	}

	// Plain type switches, not errors.As: a RequestError wraps the schema
	// errors it describes, and unwrapping it would lose where they occurred.
	switch e := err.(type) { //nolint:errorlint // see above.
	case openapi3.MultiError:
		out := make([]string, 0, len(e))

		for _, inner := range e {
			out = append(out, requestViolations(inner)...)
		}

		return out
	case *openapi3filter.RequestError:
		return describeRequestError(e)
	default:
		return []string{firstLine(err.Error())}
	}
}

// describeRequestError renders a kin-openapi request error as messages like
// `query parameter "limit": number must be at most 100` or
// `request body.quantity: property "quantity" is missing`.
func describeRequestError(re *openapi3filter.RequestError) []string {
	where := "request"

	switch {
	case re.Parameter != nil:
		where = fmt.Sprintf("%s parameter %q", re.Parameter.In, re.Parameter.Name)
	case re.RequestBody != nil:
		where = "request body"
	}

	if re.Err == nil {
		return []string{where + ": " + nonEmptyOr(re.Reason, firstLine(re.Error()))}
	}

	return describeCause(where, re.Err, re.Reason)
}

// describeCause renders the cause of a request error: schema errors name the
// failing property, anything else is reported by its first line.
func describeCause(where string, cause error, reason string) []string {
	switch c := cause.(type) { //nolint:errorlint // schema errors may be nested in a multi-error.
	case openapi3.MultiError:
		out := make([]string, 0, len(c))

		for _, inner := range c {
			out = append(out, describeCause(where, inner, reason)...)
		}

		return out
	case *openapi3.SchemaError:
		loc := where
		if p := c.JSONPointer(); len(p) > 0 {
			loc += "." + strings.Join(p, ".")
		}

		return []string{loc + ": " + nonEmptyOr(c.Reason, firstLine(c.Error()))}
	default:
		return []string{where + ": " + nonEmptyOr(firstLine(cause.Error()), reason)}
	}
}

func nonEmptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return s
}

// firstLine returns the first line of s (schema errors append the offending
// schema and value on following lines).
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}
