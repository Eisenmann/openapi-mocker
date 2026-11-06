package domain

import "time"

// Kinds of DriftDifference.
const (
	DriftStatus        = "status"          // different HTTP status.
	DriftContentType   = "content-type"    // different media type.
	DriftMissing       = "missing-in-live" // a field of the mock the upstream does not return.
	DriftExtra         = "extra-in-live"   // a field of the upstream the mock does not have.
	DriftType          = "type-mismatch"   // the field has another JSON type.
	DriftContract      = "contract"        // the upstream response breaks the contract.
	DriftBodyNotJSON   = "body-not-json"   // one side is JSON and the other is not.
	DriftUpstreamError = "upstream-error"  // the upstream could not be reached.
)

// DriftDifference is one way the upstream answer differs from the mock's.
type DriftDifference struct {
	Kind string `json:"kind"`
	// Path is the JSON path of the field ("user.roles[0].name"), empty for
	// the response as a whole.
	Path string `json:"path,omitempty"`
	Mock string `json:"mock,omitempty"`
	Live string `json:"live,omitempty"`
}

// DriftReport is the outcome of comparing one mock answer with the upstream.
type DriftReport struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	// Mock says what answered the client: "rule <id>" or "schema-example".
	Mock       string    `json:"mock"`
	MockStatus int       `json:"mockStatus"`
	LiveStatus int       `json:"liveStatus"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"durationMs"`
	// Drifted is true when there is at least one difference.
	Drifted     bool              `json:"drifted"`
	Differences []DriftDifference `json:"differences,omitempty"`
}
