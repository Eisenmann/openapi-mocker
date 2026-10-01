package httpapi

import (
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func (a *api) listProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Projects.List())
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

	writeJSON(w, http.StatusOK, p)
}

// updateProject changes project settings. Only the fields present in the
// JSON body are modified: validationMode (off, warn or enforce) and stateMode
// (off, memory or persisted).
func (a *api) updateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ValidationMode *domain.ValidationMode `json:"validationMode"`
		StateMode      *domain.StateMode      `json:"stateMode"`
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
