package httpapi

import (
	"io"
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// getState returns every collection of the project's mock state.
func (a *api) getState(w http.ResponseWriter, r *http.Request) {
	if a.s.State == nil {
		writeError(w, http.StatusBadRequest, usecase.ErrStateNotEnabled)
		return
	}

	snap, err := a.s.State.Snapshot(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

// seedState replaces one collection with the posted JSON array of objects.
func (a *api) seedState(w http.ResponseWriter, r *http.Request) {
	if a.s.State == nil {
		writeError(w, http.StatusBadRequest, usecase.ErrStateNotEnabled)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMockBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}

	n, err := a.s.State.Seed(r.PathValue("id"), r.PathValue("collection"), body)
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// resetState empties one collection ({collection...}) or, without it, every
// collection of the project.
func (a *api) resetState(w http.ResponseWriter, r *http.Request) {
	if a.s.State == nil {
		writeError(w, http.StatusBadRequest, usecase.ErrStateNotEnabled)
		return
	}

	err := a.s.State.Reset(r.PathValue("id"), r.PathValue("collection"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
