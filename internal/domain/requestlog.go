package domain

import "time"

// RequestLog is a record of a request served by the mock server.
type RequestLog struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	StatusCode  int       `json:"statusCode"`
	MatchedRule string    `json:"matchedRule,omitempty"`
	Matched     bool      `json:"matched"`
	Timestamp   time.Time `json:"timestamp"`
	DurationMs  int64     `json:"durationMs"`
	// Violations lists request-validation problems found for this request
	// (set when the project's validation mode is warn or enforce).
	Violations []string `json:"violations,omitempty"`
}
