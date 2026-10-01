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

// StateMode controls whether the project's REST mock keeps state, so that
// POST/PUT/PATCH/DELETE change what later GET requests return.
type StateMode string

const (
	// StateOff keeps no state: every response comes from mock rules or the
	// contract's schema example (the default).
	StateOff StateMode = "off"
	// StateMemory keeps collections in memory; they are lost on restart.
	StateMemory StateMode = "memory"
	// StatePersisted keeps collections on disk, so they survive restarts.
	StatePersisted StateMode = "persisted"
)

// Valid reports whether m is a known mode. The empty string means "off", so
// projects stored before this setting existed keep working.
func (m StateMode) Valid() bool {
	switch m {
	case "", StateOff, StateMemory, StatePersisted:
		return true
	default:
		return false
	}
}

// Enabled reports whether the mode keeps state.
func (m StateMode) Enabled() bool {
	return m == StateMemory || m == StatePersisted
}

// Project is an isolated workspace: one active OpenAPI contract + a set of
// mock rules served at /mock/{projectId}/...
type Project struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	ValidationMode ValidationMode `json:"validationMode"`
	StateMode      StateMode      `json:"stateMode"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}
