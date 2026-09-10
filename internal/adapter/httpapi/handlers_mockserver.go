package httpapi

import (
	"net/http"
	"time"
)

// serveMock is the HTTP delegate for usecase.MockServingService: the usecase
// decides WHAT to respond (including the required delay in ms), and this
// controller performs the actual IO — sleeping and writing to
// http.ResponseWriter. This separation keeps the usecase layer free of
// side-effects tied to real request-time execution.
func (a *api) serveMock(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	path := "/" + r.PathValue("path")
	scenario := r.Header.Get("X-Mock-Scenario")

	resp := a.s.MockServing.Serve(projectID, r.Method, path, scenario)

	if resp.DelayMs > 0 {
		time.Sleep(time.Duration(resp.DelayMs) * time.Millisecond)
	}

	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}

	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}

	if resp.Source != "" {
		w.Header().Set("X-Mock-Source", resp.Source)
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := w.Write(resp.Body); err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)
	}
}

func (a *api) serveMockRoot(w http.ResponseWriter, r *http.Request) {
	scenario := r.Header.Get("X-Mock-Scenario")

	resp := a.s.MockServing.Serve(r.PathValue("projectId"), r.Method, "/", scenario)
	if resp.DelayMs > 0 {
		time.Sleep(time.Duration(resp.DelayMs) * time.Millisecond)
	}

	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := w.Write(resp.Body); err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)
	}
}
