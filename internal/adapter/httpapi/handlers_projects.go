package httpapi

import (
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func (a *api) listProjects(w http.ResponseWriter, _ *http.Request) {
	projects := a.s.Projects.List()

	// The mock credentials of a project that requires a token are not for
	// everybody who can list projects.
	if a.s.Access != nil {
		for i, p := range projects {
			if p.Auth != nil && a.s.Access.Locked(p.ID) {
				c := *p
				c.Auth = nil
				projects[i] = &c
			}
		}
	}

	writeJSON(w, http.StatusOK, projects)
}

func (a *api) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Description string }

	err := readJSON(r, &body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	p, err := a.s.Projects.Create(body.Name, body.Description)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusCreated, p)
}

func (a *api) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.s.Projects.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, a.visibleProject(r, p))
}

// visibleProject hides the mock credentials from callers below the editor
// role.
func (a *api) visibleProject(r *http.Request, p *domain.Project) *domain.Project {
	pr := principalOf(r)
	if pr == nil || p.Auth == nil || pr.Role.Allows(domain.RoleEditor) {
		return p
	}

	c := *p
	c.Auth = nil

	return &c
}

// updateProject changes project settings. Only the fields present in the
// JSON body are modified: validationMode (off, warn or enforce) and stateMode
// (off, memory or persisted), and the proxy settings upstreamUrl, proxyMode
// (off, forward or record) and driftCheck.
func (a *api) updateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ValidationMode *domain.ValidationMode `json:"validationMode"`
		StateMode      *domain.StateMode      `json:"stateMode"`
		UpstreamURL    *string                `json:"upstreamUrl"`
		ProxyMode      *domain.ProxyMode      `json:"proxyMode"`
		DriftCheck     *bool                  `json:"driftCheck"`
	}

	err := readJSON(r, &body)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidJSON)
		return
	}

	id := r.PathValue("id")

	if body.ValidationMode != nil {
		_, err = a.s.Projects.SetValidationMode(id, *body.ValidationMode)
		if err != nil {
			writeError(w, usecase.StatusFromError(err), err)
			return
		}
	}

	if body.StateMode != nil {
		_, err = a.s.Projects.SetStateMode(id, *body.StateMode)
		if err != nil {
			writeError(w, usecase.StatusFromError(err), err)
			return
		}
	}

	if body.UpstreamURL != nil || body.ProxyMode != nil || body.DriftCheck != nil {
		_, err = a.s.Projects.UpdateProxy(id, usecase.ProxyPatch{
			UpstreamURL: body.UpstreamURL, Mode: body.ProxyMode, DriftCheck: body.DriftCheck,
		})
		if err != nil {
			writeError(w, usecase.StatusFromError(err), err)
			return
		}
	}

	a.getProject(w, r)
}

func (a *api) deleteProject(w http.ResponseWriter, r *http.Request) {
	err := a.s.Projects.Delete(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
