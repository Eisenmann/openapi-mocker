package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	headerShareToken = "X-Share-Token" //nolint:gosec // a header name, not a credential.
	querySharedToken = "share"
	actionDenied     = "access.denied"
	bearerScheme     = "bearer "
)

// noProject marks a mock that exists but belongs to no project.
const noProject = "\x00"

// requestAudit carries what a handler wants to add to its audit entry.
type requestAudit struct {
	principal usecase.Principal
	note      string
}

type auditKey struct{}

// setAuditNote adds a detail (a version number, a name) to the audit entry
// of the current request. It does nothing when the request is not audited.
func setAuditNote(r *http.Request, note string) {
	if ra, ok := r.Context().Value(auditKey{}).(*requestAudit); ok {
		ra.note = note
	}
}

// principalOf returns who made the request (nil when access control is off
// or the route is outside the project-scoped API).
func principalOf(r *http.Request) *usecase.Principal {
	if ra, ok := r.Context().Value(auditKey{}).(*requestAudit); ok {
		return &ra.principal
	}

	return nil
}

// credentialsOf reads the bearer token and the share-link secret.
func credentialsOf(r *http.Request) usecase.Credentials {
	cred := usecase.Credentials{Bearer: "", Share: r.Header.Get(headerShareToken)}

	h := r.Header.Get("Authorization")
	if len(h) > len(bearerScheme) && strings.EqualFold(h[:len(bearerScheme)], bearerScheme) {
		cred.Bearer = strings.TrimSpace(h[len(bearerScheme):])
	}

	if cred.Share == "" && isRead(r.Method) {
		cred.Share = r.URL.Query().Get(querySharedToken)
	}

	return cred
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter

	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	err := http.NewResponseController(s.ResponseWriter).Flush()
	_ = err
}

// withAccess enforces roles on the project-scoped management API and audits
// its changes. A project without API tokens stays open. Routes outside that
// API (the mock server, imports, providers) are not touched.
func (a *api) withAccess(mux *http.ServeMux, next http.Handler) http.Handler {
	if a.s.Access == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		_, pattern := mux.Handler(r)
		rule, scoped := ruleFor(pattern)

		projectID := ""
		if scoped {
			projectID = a.scopeProject(r, pattern)
		}

		if projectID == noProject {
			// A mock without a project cannot be authorized: refuse.
			writeError(w, http.StatusForbidden, usecase.ErrForbidden)
			return
		}

		if projectID == "" {
			next.ServeHTTP(w, r)
			return
		}

		a.serveScoped(w, r, next, projectID, &rule)
	})
}

// scopeProject returns the project a scoped request is about ("" when it
// cannot be told, in which case the handler answers 404).
func (a *api) scopeProject(r *http.Request, pattern string) string {
	_, path := splitPattern(pattern)

	if strings.HasPrefix(path, mockRoutePrefix) {
		// Mock routes name the mock; its project is looked up.
		id := strings.TrimPrefix(r.URL.Path, "/api/mocks/")
		project, found := a.s.Access.ProjectOfMock(strings.SplitN(id, "/", 2)[0]) //nolint:mnd // id, rest.

		if found && project == "" {
			return noProject
		}

		return project
	}

	// /api/projects/{id}/...
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/", 2) //nolint:mnd // id, rest.

	return parts[0]
}

// serveScoped authenticates the caller, checks the role, runs the handler
// and writes the audit entry.
func (a *api) serveScoped(
	w http.ResponseWriter, r *http.Request, next http.Handler, projectID string, rule *accessRule,
) {
	principal, err := a.s.Access.Authenticate(projectID, credentialsOf(r))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="openapi-mocker"`)
		writeError(w, http.StatusUnauthorized, err)

		return
	}

	if !principal.Role.Allows(rule.need) || (principal.Share && !rule.share) {
		if !isRead(r.Method) {
			a.audit(r, projectID, &auditCall{
				principal: &principal, action: actionDenied, rule: rule, status: http.StatusForbidden,
				note: fmt.Sprintf("needs role %s", rule.need),
			})
		}

		writeError(w, http.StatusForbidden, usecase.ErrForbidden)

		return
	}

	ra := &requestAudit{principal: principal, note: ""}
	r = r.WithContext(context.WithValue(r.Context(), auditKey{}, ra))

	if isRead(r.Method) || rule.action == "" {
		next.ServeHTTP(w, r)
		return
	}

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(rec, r)

	if rec.status < http.StatusBadRequest {
		a.audit(r, projectID, &auditCall{
			principal: &principal, action: rule.action, rule: rule, status: rec.status, note: ra.note,
		})
	}
}

// auditCall is one entry to write.
type auditCall struct {
	principal *usecase.Principal
	action    string
	rule      *accessRule
	status    int
	note      string
}

func (a *api) audit(r *http.Request, projectID string, c *auditCall) {
	targetID := ""
	if c.rule.targetParam != "" {
		targetID = r.PathValue(c.rule.targetParam)
	}

	a.s.Access.Record(&domain.AuditEntry{
		ID: "", Time: time.Time{}, ProjectID: projectID, Actor: c.principal.Actor, Action: c.action,
		TargetType: c.rule.targetType, TargetID: targetID, Method: r.Method, Path: r.URL.Path,
		Status: c.status, Note: c.note, RemoteAddr: remoteHost(r),
	})
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
