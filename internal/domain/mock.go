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
	Fault     *Fault    `json:"fault,omitempty"`
	IsLLMGen  bool      `json:"isLlmGenerated"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
