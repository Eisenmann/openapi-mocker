package httpapi

import (
	"net/http"
	"strconv"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// verifyStrictParam makes a failed verification answer 409 instead of 200.
const verifyStrictParam = "strict"

func (a *api) registerVerifyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{id}/verify", a.verifyCalls)
	mux.HandleFunc("GET /api/projects/{id}/calls", a.listCalls)
	mux.HandleFunc("DELETE /api/projects/{id}/calls", a.clearCalls)
}

// verifyCalls answers "was this call made to the mock, and how often?" from
// the recorded calls. The answer is 200 with passed=true|false; with
// ?strict=true a failed verification is 409, so a plain curl or HTTP client
// call can fail a test on its own.
func (a *api) verifyCalls(w http.ResponseWriter, r *http.Request) {
	var req usecase.VerifyRequest

	err := readJSON(r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	res, err := a.s.Logs.Verify(r.PathValue("id"), &req)
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	status := http.StatusOK
	if !res.Passed && r.URL.Query().Get(verifyStrictParam) == "true" {
		status = http.StatusConflict
	}

	writeJSON(w, status, res)
}

// listCalls returns the recorded calls with their request and response
// bodies, newest first. ?limit= caps the number.
func (a *api) listCalls(w http.ResponseWriter, r *http.Request) {
	limit := defaultLogLimit

	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errInvalidLimit)
			return
		}

		limit = min(n, usecase.JournalPerProject)
	}

	calls, err := a.s.Logs.Calls(r.PathValue("id"), limit)
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, calls)
}

// clearCalls forgets the recorded calls, so a test starts from a clean slate.
func (a *api) clearCalls(w http.ResponseWriter, r *http.Request) {
	_, err := a.s.Logs.ClearCalls(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
