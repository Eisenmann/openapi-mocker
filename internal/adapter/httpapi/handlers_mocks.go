package httpapi

import (
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func (a *api) listMocks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Mocks.List(r.PathValue("id")))
}

func (a *api) createMock(w http.ResponseWriter, r *http.Request) {
	var m domain.MockRule

	err := readJSON(w, r, &m)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	m.ProjectID = r.PathValue("id")

	created, err := a.s.Mocks.Create(&m)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

func (a *api) updateMock(w http.ResponseWriter, r *http.Request) {
	var m domain.MockRule

	err := readJSON(w, r, &m)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	m.ID = r.PathValue("mockId")

	err = a.s.Mocks.Update(&m)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, m)
}

func (a *api) deleteMock(w http.ResponseWriter, r *http.Request) {
	err := a.s.Mocks.Delete(r.PathValue("mockId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *api) generateMock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path       string `json:"path"`
		Method     string `json:"method"`
		StatusCode int    `json:"statusCode"`
		ProviderID string `json:"providerId"`
		Hints      string `json:"hints"`
	}

	err := readJSON(w, r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	body, warning, err := a.s.Mocks.GenerateBody(
		r.Context(), r.PathValue("id"),
		req.Path, req.Method,
		req.StatusCode,
		req.ProviderID, req.Hints,
	)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"body": body, "warning": warning})
}
