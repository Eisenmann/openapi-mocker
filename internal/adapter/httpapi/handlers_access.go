package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	defaultAuditLimit = 100
	maxAuditLimit     = 1000
)

func (a *api) registerAccessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/access/whoami", a.whoami)
	mux.HandleFunc("GET /api/projects/{id}/access/tokens", a.listTokens)
	mux.HandleFunc("POST /api/projects/{id}/access/tokens", a.createToken)
	mux.HandleFunc("DELETE /api/projects/{id}/access/tokens/{tokenId}", a.revokeToken)
	mux.HandleFunc("GET /api/projects/{id}/access/share-links", a.listShareLinks)
	mux.HandleFunc("POST /api/projects/{id}/access/share-links", a.createShareLink)
	mux.HandleFunc("DELETE /api/projects/{id}/access/share-links/{linkId}", a.revokeShareLink)
	mux.HandleFunc("GET /api/projects/{id}/audit", a.listAudit)
	mux.HandleFunc("GET /api/share/{token}", a.viewShare)
}

// actorOf returns who makes the request.
func actorOf(r *http.Request) domain.Actor {
	if p := principalOf(r); p != nil {
		return p.Actor
	}

	return domain.Anonymous()
}

func (a *api) accessEnabled(w http.ResponseWriter) bool {
	if a.s.Access == nil {
		writeError(w, http.StatusNotFound, errAccessNotEnabled)
		return false
	}

	return true
}

// whoami tells the caller how the project sees them.
func (a *api) whoami(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	p := principalOf(r)
	if p == nil {
		writeError(w, http.StatusInternalServerError, errAccessNotEnabled)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"actor": p.Actor, "role": p.Role, "share": p.Share, "locked": a.s.Access.Locked(r.PathValue("id")),
	})
}

func (a *api) listTokens(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"locked": a.s.Access.Locked(r.PathValue("id")), "tokens": a.s.Access.ListTokens(r.PathValue("id")),
	})
}

// createToken makes an API token; its secret is in the response and nowhere else.
func (a *api) createToken(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	var body struct {
		Name    string      `json:"name"`
		Role    domain.Role `json:"role"`
		TTLDays int         `json:"ttlDays"`
	}

	err := readJSON(r, &body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	created, err := a.s.Access.CreateToken(r.PathValue("id"),
		usecase.TokenInput{Name: body.Name, Role: body.Role, TTLDays: body.TTLDays}, actorOf(r))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	setAuditNote(r, fmt.Sprintf("%s (%s, role %s)", created.Info.Name, created.Info.Prefix, created.Info.Role))
	writeJSON(w, http.StatusCreated, created)
}

func (a *api) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	err := a.s.Access.RevokeToken(r.PathValue("id"), r.PathValue("tokenId"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listShareLinks(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"links": a.s.Access.ListShareLinks(r.PathValue("id"))})
}

// createShareLink makes a read-only share link; the response holds its secret
// and the URL to hand out.
func (a *api) createShareLink(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	var body struct {
		Label   string `json:"label"`
		TTLDays int    `json:"ttlDays"`
	}

	err := readJSON(r, &body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	created, err := a.s.Access.CreateShareLink(r.PathValue("id"),
		usecase.LinkInput{Label: body.Label, TTLDays: body.TTLDays}, actorOf(r))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	setAuditNote(r, fmt.Sprintf("%s (%s)", created.Info.Label, created.Info.Prefix))
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": created.Secret, "info": created.Info, "url": "/api/share/" + created.Secret,
	})
}

func (a *api) revokeShareLink(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	err := a.s.Access.RevokeShareLink(r.PathValue("id"), r.PathValue("linkId"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// listAudit returns the project's audit log, newest first. Query parameters:
// limit, action, actor (a token name or id) and since (RFC 3339).
func (a *api) listAudit(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	q := usecase.AuditQuery{
		Limit: defaultAuditLimit, Action: r.URL.Query().Get("action"), Actor: r.URL.Query().Get("actor"), Since: time.Time{},
	}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errInvalidLimit)
			return
		}

		q.Limit = min(n, maxAuditLimit)
	}

	if raw := r.URL.Query().Get("since"); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errInvalidSince)
			return
		}

		q.Since = since
	}

	writeJSON(w, http.StatusOK, a.s.Access.ListAudit(r.PathValue("id"), &q))
}

// viewShare shows what a share link gives access to: the project's contract
// and mocks, read-only, in one document. The secret in the URL is the only
// credential.
func (a *api) viewShare(w http.ResponseWriter, r *http.Request) {
	if !a.accessEnabled(w) {
		return
	}

	link, err := a.s.Access.ResolveShare(r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	p, err := a.s.Projects.Get(link.ProjectID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	view := map[string]any{
		"readOnly":  true,
		"link":      map[string]any{"label": link.Label, "expiresAt": link.ExpiresAt},
		"project":   map[string]any{"id": p.ID, "name": p.Name, "description": p.Description, "updatedAt": p.UpdatedAt},
		"mocks":     a.s.Mocks.List(p.ID),
		"contract":  nil,
		"endpoints": []usecase.Endpoint{},
	}

	if c, cerr := a.s.Contracts.GetActive(p.ID); cerr == nil {
		view["contract"] = c
		if eps, eerr := a.s.Contracts.ListEndpoints(p.ID); eerr == nil {
			view["endpoints"] = eps
		}
	}

	writeJSON(w, http.StatusOK, view)
}
