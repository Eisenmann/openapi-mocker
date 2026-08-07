package domain

import "time"

// MockRule is a response rule for a specific path+method(+scenario) of a project.
type MockRule struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"projectId"`
	Path        string            `json:"path"`   // e.g. /users/{id}.
	Method      string            `json:"method"` // GET, POST...
	Scenario    string            `json:"scenario"`
	StatusCode  int               `json:"statusCode"`
	ContentType string            `json:"contentType"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	DelayMs     int               `json:"delayMs"`
	FailRatePct int               `json:"failRatePct"` // % of random 5xx errors for chaos testing.
	IsLLMGen    bool              `json:"isLlmGenerated"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}
