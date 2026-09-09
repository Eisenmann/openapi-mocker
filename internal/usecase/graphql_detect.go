package usecase

import "strings"

// GraphQL SDL content-sniffing heuristic. The detection must be conservative:
// an OpenAPI document containing a GraphQL snippet inside a description must
// NOT be misdetected as GraphQL, and extension-only/directive-only SDL must
// be recognized as GraphQL.
func IsGraphQL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}

	// OpenAPI / Swagger JSON and YAML documents are never GraphQL.
	if strings.HasPrefix(trimmed, `{"openapi"`) ||
		strings.HasPrefix(trimmed, `{"swagger"`) ||
		strings.HasPrefix(trimmed, "openapi:") ||
		strings.HasPrefix(trimmed, "swagger:") {
		return false
	}

	// Check the first non-empty line for a GraphQL SDL top-level keyword.
	// GraphQL documents always start with one of these definitions.
	firstLine := trimmed
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		firstLine = trimmed[:idx]
	}

	for _, kw := range []string{"type ", "schema ", "interface ", "union ", "enum ", "input ", "scalar ", "extend ", "directive @", "directive "} {
		if strings.HasPrefix(firstLine, kw) {
			return true
		}
	}

	return false
}
