package httpapi

import (
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func (a *api) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/auth", a.getAuth)
	mux.HandleFunc("PUT /api/projects/{id}/auth", a.putAuth)
	mux.HandleFunc("DELETE /api/projects/{id}/auth", a.deleteAuth)
}

// guardEndpoint applies the auth simulation to a GraphQL or MCP request. It
// writes the 401/403 response and reports true when the request is rejected.
func (a *api) guardEndpoint(w http.ResponseWriter, r *http.Request) bool {
	denied := a.s.Auth.AuthorizeEndpoint(r.PathValue("projectId"), &usecase.MockRequest{
		Method: r.Method, Path: r.URL.Path, Scenario: "", Query: r.URL.Query(), Header: r.Header,
		Body: nil, SkipValidation: true, Context: r.Context(),
	})
	if denied == nil {
		return false
	}

	for k, v := range denied.Headers {
		w.Header().Set(k, v)
	}

	w.Header().Set("Content-Type", denied.ContentType)
	w.Header().Set("X-Mock-Source", denied.Source)
	w.WriteHeader(denied.StatusCode)
	_, _ = w.Write(denied.Body)

	return true
}

// getAuth returns the project's auth simulation settings (mode "off" when
// nothing is configured).
func (a *api) getAuth(w http.ResponseWriter, r *http.Request) {
	p, err := a.s.Projects.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	if p.Auth == nil {
		writeJSON(w, http.StatusOK, domain.NoAuth())
		return
	}

	writeJSON(w, http.StatusOK, p.Auth)
}

// putAuth replaces the auth simulation settings.
func (a *api) putAuth(w http.ResponseWriter, r *http.Request) {
	var cfg domain.AuthConfig

	err := readJSON(r, &cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidJSON)
		return
	}

	p, err := a.s.Projects.SetAuth(r.PathValue("id"), &cfg)
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, p.Auth)
}

// deleteAuth removes the auth simulation settings, credentials included.
func (a *api) deleteAuth(w http.ResponseWriter, r *http.Request) {
	_, err := a.s.Projects.SetAuth(r.PathValue("id"), nil)
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
