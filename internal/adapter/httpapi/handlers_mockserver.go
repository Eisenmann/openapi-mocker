package httpapi

import (
	"io"
	"net/http"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// maxMockBodyBytes caps the request body accepted by the mock server.
const maxMockBodyBytes = 10 << 20

// serveMock is the HTTP delegate for usecase.MockServingService: the usecase
// decides WHAT to respond (including the required delay in ms), and this
// controller performs the actual IO — sleeping and writing to
// http.ResponseWriter. This separation keeps the usecase layer free of
// side-effects tied to real request-time execution.
func (a *api) serveMock(w http.ResponseWriter, r *http.Request) {
	a.handleMock(w, r, "/"+r.PathValue("path"))
}

func (a *api) serveMockRoot(w http.ResponseWriter, r *http.Request) {
	a.handleMock(w, r, "/")
}

// handleMock hands the full request (query, headers and body, so the usecase
// can validate it against the contract) to the usecase and writes the result.
func (a *api) handleMock(w http.ResponseWriter, r *http.Request, path string) {
	defer r.Body.Close()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMockBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}

	resp := a.s.MockServing.ServeRequest(r.PathValue("projectId"), &usecase.MockRequest{
		Method:   r.Method,
		Path:     path,
		Scenario: r.Header.Get("X-Mock-Scenario"),
		Query:    r.URL.Query(),
		Header:   r.Header,
		Body:     body,

		SkipValidation: false,
	})

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

	_, err = w.Write(resp.Body)
	if err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)

		return
	}
}
