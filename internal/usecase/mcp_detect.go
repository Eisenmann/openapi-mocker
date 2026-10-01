package usecase

import (
	"encoding/json"
	"strings"
)

// IsMCP reports whether raw looks like an MCP server manifest contract.
// The detection is conservative and must not misfire on:
//   - OpenAPI documents (they have "openapi"/"swagger" roots);
//   - GraphQL SDL (not JSON at all);
//   - arbitrary JSON payloads (they lack the MCP markers).
//
// An MCP contract is a JSON object that either declares an "mcpServer" root
// (the canonical marker used by openapi-mocker) or combines "tools" with a
// "serverInfo" block (used by some external MCP manifests).
func IsMCP(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}

	var probe struct {
		MCPServer  json.RawMessage `json:"mcpServer"`
		Tools      json.RawMessage `json:"tools"`
		ServerInfo json.RawMessage `json:"serverInfo"`
	}

	// A malformed JSON document is not detected as MCP; the engine's
	// ParseAndValidate will report the syntax error to the user instead.
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return false
	}

	if isPresent(probe.MCPServer) {
		return true
	}

	return isPresent(probe.Tools) && isPresent(probe.ServerInfo)
}

// isPresent reports whether a probed JSON field carries an actual value:
// encoding/json decodes explicit nulls into the literal bytes "null" inside
// json.RawMessage, so a bare nil check would misread null markers as data.
func isPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}
