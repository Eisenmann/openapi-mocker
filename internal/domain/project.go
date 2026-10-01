package domain

import "time"

// ValidationMode controls how incoming mock requests are checked against the
// project's contract (OpenAPI operation or MCP tool input schema).
type ValidationMode string

const (
	// ValidationOff serves every request without checking it (the default).
	ValidationOff ValidationMode = "off"
	// ValidationWarn serves the mock but reports violations in the request
	// log and in a response header.
	ValidationWarn ValidationMode = "warn"
	// ValidationEnforce rejects invalid requests (HTTP 400 for REST,
	// JSON-RPC -32602 for MCP) with a description of the violations.
	ValidationEnforce ValidationMode = "enforce"
)

// Valid reports whether m is one of the known modes. The empty string is
// accepted as "off" so projects stored before this setting existed keep
// working.
func (m ValidationMode) Valid() bool {
	switch m {
	case "", ValidationOff, ValidationWarn, ValidationEnforce:
		return true
	default:
		return false
	}
}

// Project is an isolated workspace: one active OpenAPI contract + a set of
// mock rules served at /mock/{projectId}/...
type Project struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	ValidationMode ValidationMode `json:"validationMode"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}
