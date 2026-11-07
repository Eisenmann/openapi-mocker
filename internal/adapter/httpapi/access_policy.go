package httpapi

import (
	"net/http"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Route prefixes of the project-scoped management API.
const (
	projectRoutePrefix = "/api/projects/{id}"
	mockRoutePrefix    = "/api/mocks/{mockId}"
)

// Audit action names and target types that several rules share.
const (
	targetProject  = "project"
	targetContract = "contract"
	targetMock     = "mock"
)

// accessRule says who may call a route and how a call is audited.
type accessRule struct {
	// need is the lowest role that may call the route.
	need domain.Role
	// share is set when a share link may call the route.
	share bool
	// action names the change in the audit log; empty means "not audited".
	action     string
	targetType string
	// targetParam is the path parameter holding the target's id.
	targetParam string
}

// newRule builds an accessRule with every field set.
func newRule(need domain.Role, share bool, action, targetType, targetParam string) accessRule {
	return accessRule{need: need, share: share, action: action, targetType: targetType, targetParam: targetParam}
}

// plain is a rule for a route that is not audited and not open to share links.
func plain(need domain.Role) accessRule {
	return newRule(need, false, "", "", "")
}

// change is a rule for an audited change.
func change(need domain.Role, action, targetType, targetParam string) accessRule {
	return newRule(need, false, action, targetType, targetParam)
}

// splitPattern separates "GET /api/x" into method and path.
func splitPattern(pattern string) (method, path string) {
	method, path, found := strings.Cut(pattern, " ")
	if !found {
		return "", pattern
	}

	return method, path
}

// isRead reports whether the method only reads.
func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// shareReadable lists the read routes a share link may call, relative to
// the project route.
var shareReadable = map[string]bool{ //nolint:gochecknoglobals // a constant table.
	"/contract":                    true,
	"/contract/versions":           true,
	"/contract/versions/{version}": true,
	"/contract/diff":               true,
	"/contract/export":             true,
	"/endpoints":                   true,
	"/mocks":                       true,
	"/mcp/tools":                   true,
	"/mcp/catalog":                 true,
	"/graphql/schema":              true,
	"/graphql/operations":          true,
}

// auditActions names the audited changes by "METHOD route".
var auditActions = buildAuditTable() //nolint:gochecknoglobals // a constant table.

func buildAuditTable() map[string]accessRule {
	table := map[string]accessRule{}
	add := func(method, route string, r accessRule) { table[method+" "+route] = r }

	proj := projectRoutePrefix
	mock := mockRoutePrefix

	add(http.MethodPatch, proj, change(domain.RoleAdmin, "project.update", targetProject, ""))
	add(http.MethodDelete, proj, change(domain.RoleAdmin, "project.delete", targetProject, ""))
	add(http.MethodPost, proj+"/contract", change(domain.RoleEditor, "contract.publish", targetContract, ""))
	add(http.MethodPost, proj+"/contract/versions/{version}/rollback",
		change(domain.RoleEditor, "contract.rollback", targetContract, "version"))
	add(http.MethodPost, proj+"/mocks", change(domain.RoleEditor, "mock.create", targetMock, ""))
	add(http.MethodPut, mock, change(domain.RoleEditor, "mock.update", targetMock, "mockId"))
	add(http.MethodDelete, mock, change(domain.RoleEditor, "mock.delete", targetMock, "mockId"))
	add(http.MethodPut, proj+"/auth", change(domain.RoleEditor, "auth.update", "auth", ""))
	add(http.MethodDelete, proj+"/auth", change(domain.RoleEditor, "auth.delete", "auth", ""))
	add(http.MethodPut, proj+"/state/{collection...}", change(domain.RoleEditor, "state.seed", "state", "collection"))
	add(http.MethodDelete, proj+"/state", change(domain.RoleEditor, "state.reset", "state", ""))
	add(http.MethodDelete, proj+"/state/{collection...}",
		change(domain.RoleEditor, "state.reset", "state", "collection"))
	add(http.MethodDelete, proj+"/drift", change(domain.RoleEditor, "drift.clear", "drift", ""))
	add(http.MethodPost, proj+"/access/tokens", change(domain.RoleAdmin, "token.create", "token", ""))
	add(http.MethodDelete, proj+"/access/tokens/{tokenId}",
		change(domain.RoleAdmin, "token.revoke", "token", "tokenId"))
	add(http.MethodPost, proj+"/access/share-links",
		change(domain.RoleAdmin, "share-link.create", "share-link", ""))
	add(http.MethodDelete, proj+"/access/share-links/{linkId}",
		change(domain.RoleAdmin, "share-link.revoke", "share-link", "linkId"))

	return table
}

// notAudited lists project routes that use POST or similar without changing
// anything (they validate or generate text).
var notAudited = map[string]bool{ //nolint:gochecknoglobals // a constant table.
	http.MethodPost + " " + projectRoutePrefix + "/contract/validate": true,
	http.MethodPost + " " + projectRoutePrefix + "/contract/generate": true,
	http.MethodPost + " " + projectRoutePrefix + "/mocks/generate":    true,
}

// ruleFor returns the access rule of a route pattern. ok is false for routes
// outside the project-scoped API (they are not access-controlled).
func ruleFor(pattern string) (rule accessRule, ok bool) {
	method, path := splitPattern(pattern)

	scoped := strings.HasPrefix(path, projectRoutePrefix) || strings.HasPrefix(path, mockRoutePrefix)
	if !scoped {
		return plain(domain.RoleViewer), false
	}

	if r, audited := auditActions[pattern]; audited {
		return r, true
	}

	return relativeRule(method, strings.TrimPrefix(path, projectRoutePrefix), pattern), true
}

// relativeRule is the rule of a route that is not in the audit table.
func relativeRule(method, rel, pattern string) accessRule {
	switch {
	case strings.HasPrefix(rel, "/access"):
		return accessRuleFor(method, rel)
	case rel == "/audit":
		return plain(domain.RoleViewer)
	case rel == "/auth" || rel == "/export":
		// Mock credentials and the whole bundle: more than a reader sees.
		return plain(domain.RoleEditor)
	case isRead(method):
		return newRule(domain.RoleViewer, shareReadable[rel], "", "", "")
	case notAudited[pattern]:
		return plain(domain.RoleViewer)
	default:
		return change(domain.RoleEditor, "other: "+pattern, "", "")
	}
}

// accessRuleFor is the rule of the access-management routes: admins only,
// except whoami, which anybody who is let in may call.
func accessRuleFor(method, rel string) accessRule {
	if method == http.MethodGet && rel == "/access/whoami" {
		return newRule(domain.RoleViewer, true, "", "", "")
	}

	return plain(domain.RoleAdmin)
}
