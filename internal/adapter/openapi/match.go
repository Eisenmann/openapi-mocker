package openapi

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

type matchedOperation struct {
	PathTemplate string
	Operation    *openapi3.Operation
}

// findOperation searches for an operation matching the method and actual path,
// supporting templates like /users/{id}/orders/{orderId}. When multiple
// matches are found, the template with the most static segments is chosen
// (the most specific match), e.g. /users/me takes priority over /users/{id}.
func findOperation(doc *openapi3.T, method, actualPath string) *matchedOperation {
	if doc == nil || doc.Paths == nil {
		return nil
	}

	method = strings.ToUpper(method)

	var best *matchedOperation

	bestStaticSegments := -1

	for tmpl, item := range doc.Paths.Map() {
		if !matchPath(tmpl, actualPath) {
			continue
		}

		op := item.GetOperation(method)
		if op == nil {
			continue
		}

		staticSegments := countStaticSegments(tmpl)
		if staticSegments > bestStaticSegments {
			bestStaticSegments = staticSegments
			best = &matchedOperation{PathTemplate: tmpl, Operation: op}
		}
	}

	return best
}

func countStaticSegments(tmpl string) int {
	n := 0

	for _, seg := range splitPath(tmpl) {
		if !isParam(seg) {
			n++
		}
	}

	return n
}

func isParam(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{}
	}

	return strings.Split(p, "/")
}

func matchPath(tmpl, actual string) bool {
	tParts := splitPath(tmpl)

	aParts := splitPath(actual)
	if len(tParts) != len(aParts) {
		return false
	}

	for i, tp := range tParts {
		if isParam(tp) {
			continue
		}

		if tp != aParts[i] {
			return false
		}
	}

	return true
}
