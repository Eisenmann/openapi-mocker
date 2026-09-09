package httpapi

import (
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func (a *api) listProvidersForProject(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Providers.List(r.PathValue("id")))
}

func (a *api) listProvidersGlobal(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Providers.List(""))
}

func (a *api) createProvider(w http.ResponseWriter, r *http.Request) {
	var p domain.LLMProvider

	err := readJSON(w, r, &p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	created, err := a.s.Providers.Create(&p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

func (a *api) updateProvider(w http.ResponseWriter, r *http.Request) {
	var p domain.LLMProvider

	err := readJSON(w, r, &p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	p.ID = r.PathValue("providerId")

	err = a.s.Providers.Update(&p)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (a *api) deleteProvider(w http.ResponseWriter, r *http.Request) {
	err := a.s.Providers.Delete(r.PathValue("providerId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *api) testProvider(w http.ResponseWriter, r *http.Request) {
	err := a.s.Providers.Test(r.Context(), r.PathValue("providerId"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
