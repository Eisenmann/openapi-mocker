package httpapi

import (
	"net/http"
	"strconv"
)

const (
	defaultDriftLimit = 100
	maxDriftLimit     = 1000
)

func (a *api) registerDiagnosticsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/logs", a.listLogs)
	mux.HandleFunc("GET /api/projects/{id}/drift", a.listDrift)
	mux.HandleFunc("DELETE /api/projects/{id}/drift", a.clearDrift)
}

// listDrift returns the project's contract-drift reports, newest first.
// The optional limit query parameter caps how many are returned.
func (a *api) listDrift(w http.ResponseWriter, r *http.Request) {
	limit := defaultDriftLimit

	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errInvalidLimit)
			return
		}

		limit = min(n, maxDriftLimit)
	}

	writeJSON(w, http.StatusOK, a.s.Drift.List(r.PathValue("id"), limit))
}

func (a *api) clearDrift(w http.ResponseWriter, r *http.Request) {
	a.s.Drift.Clear(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}
