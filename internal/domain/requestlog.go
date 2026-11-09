package domain

import "time"

// RequestLog is a record of a request served by the mock server.
type RequestLog struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	// Query is the encoded query string of the request (keys sorted), without
	// the leading "?"; empty when there was none.
	Query       string    `json:"query,omitempty"`
	StatusCode  int       `json:"statusCode"`
	MatchedRule string    `json:"matchedRule,omitempty"`
	Matched     bool      `json:"matched"`
	Timestamp   time.Time `json:"timestamp"`
	DurationMs  int64     `json:"durationMs"`
	// Violations lists request-validation problems found for this request
	// (set when the project's validation mode is warn or enforce).
	Violations []string `json:"violations,omitempty"`
	// Fault names the fault injected into this request (drop, timeout,
	// error, garbage, truncated or empty); empty for a normal response.
	Fault string `json:"fault,omitempty"`
	// Source says what answered the request: "rule", "schema-example",
	// "request-validation", a chaos source, or "proxy-forward",
	// "proxy-record" or "proxy-error".
	Source string `json:"source,omitempty"`
	// Endpoint is the contract operation that served the request: the path
	// template for REST ("/users/{id}"), "graphql" for GraphQL and the
	// method (plus tool name) for MCP. Empty when no operation matched.
	Endpoint string `json:"endpoint,omitempty"`
	// Captured is set when request and response bodies were captured. The
	// captures themselves are kept in memory only, so they are returned by the
	// stream and the detail endpoint and are never written to the data file.
	Captured bool         `json:"captured,omitempty"`
	Request  *HTTPCapture `json:"request,omitempty"`
	Response *HTTPCapture `json:"response,omitempty"`
}

// HTTPCapture is the captured part of one side of an exchange.
type HTTPCapture struct {
	// Headers holds the headers, with secrets replaced by "[redacted]".
	Headers map[string][]string `json:"headers,omitempty"`
	// Body is the body as text; empty when Binary is set.
	Body string `json:"body,omitempty"`
	// Size is the full size of the body in bytes.
	Size int `json:"size"`
	// Truncated is set when Body holds only the first part of the body.
	Truncated bool `json:"truncated,omitempty"`
	// Binary is set when the body is not valid UTF-8 text and was left out.
	Binary bool `json:"binary,omitempty"`
}
