package domain

import "time"

// MockRule is a response rule for a specific path+method(+scenario) of a project.
type MockRule struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`   // e.g. /users/{id}.
	Method    string `json:"method"` // GET, POST...
	Scenario  string `json:"scenario"`
	// Match lists conditions on the request; the rule is picked when all of
	// them hold, so clients need no X-Mock-Scenario header.
	Match       []MatchCondition  `json:"match,omitempty"`
	StatusCode  int               `json:"statusCode"`
	ContentType string            `json:"contentType"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	DelayMs     int               `json:"delayMs"`     // base latency of every response.
	FailRatePct int               `json:"failRatePct"` // % of injected error responses.
	// Fault holds the rest of the fault injection: latency jitter, timeouts,
	// dropped connections and malformed bodies. Nil means none.
	Fault *Fault `json:"fault,omitempty"`
	// Stream turns the response into a Server-Sent Events stream that plays
	// this timeline; Body is then ignored.
	Stream *StreamScript `json:"stream,omitempty"`
	// WebSocket answers a WebSocket upgrade request of the operation with this
	// script. Plain requests to the same operation get the rule's normal response.
	WebSocket *WSScript `json:"webSocket,omitempty"`
	// Raw serves the body and header values verbatim, without filling in
	// {{placeholders}}. Recorded rules are raw, since the upstream's data may
	// contain "{{".
	Raw bool `json:"raw,omitempty"`
	// Recording is set when the rule was saved from an upstream response.
	Recording *Recording `json:"recording,omitempty"`
	IsLLMGen  bool       `json:"isLlmGenerated"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}
