package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
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

	//nolint:contextcheck // the request context travels in MockRequest.Context.
	resp := a.s.MockServing.ServeRequest(r.PathValue("projectId"), &usecase.MockRequest{
		Method:   r.Method,
		Path:     path,
		Scenario: r.Header.Get("X-Mock-Scenario"),
		Query:    r.URL.Query(),
		Header:   r.Header,
		Body:     body,
		Context:  r.Context(),

		SkipValidation: false,
	})

	if injectConnectionFault(w, r, resp.DelayMs, resp.Fault, resp.HoldMs) {
		return
	}

	setMockHeaders(w, &resp)

	if resp.Fault == usecase.FaultTruncated {
		writeTruncated(w, resp.StatusCode, resp.Body)

		return
	}

	if resp.WebSocket != nil {
		serveWebSocket(w, r, resp.WebSocket)

		return
	}

	if resp.Stream != nil {
		serveEventStream(w, r, &resp)

		return
	}

	w.WriteHeader(resp.StatusCode)

	_, err = w.Write(resp.Body)
	if err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)

		return
	}
}

// setMockHeaders copies the headers of a mock response, plus the ones that
// say where it came from.
func setMockHeaders(w http.ResponseWriter, resp *usecase.MockResponse) {
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}

	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}

	if resp.Source != "" {
		w.Header().Set("X-Mock-Source", resp.Source)
	}

	if resp.Fault != "" {
		w.Header().Set("X-Mock-Fault", resp.Fault)
	}
}

// injectConnectionFault waits out the injected latency and then acts on the
// faults that need the connection: it drops it (FaultDrop), or holds the
// request and then drops it (FaultTimeout). It reports whether the request
// is over, so the caller must not write a response.
func injectConnectionFault(w http.ResponseWriter, r *http.Request, delayMs int, fault string, holdMs int) bool {
	if !sleepContext(r.Context(), delayMs) {
		return true // the client gave up while the mock was "slow".
	}

	switch fault {
	case usecase.FaultDrop:
		dropConnection(w)

		return true
	case usecase.FaultTimeout:
		sleepContext(r.Context(), holdMs)
		dropConnection(w)

		return true
	default:
		return false
	}
}

// sleepContext waits for ms milliseconds and reports false if the request was
// cancelled first.
func sleepContext(ctx context.Context, ms int) bool {
	if ms <= 0 {
		return true
	}

	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()

	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// writeTruncated promises the whole body but sends only half of it and then
// drops the connection, so the client sees an unexpected EOF.
func writeTruncated(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)

	_, _ = w.Write(body[:len(body)/2])

	dropConnection(w)
}

// dropConnection closes the client's TCP connection without finishing the
// response; with SO_LINGER 0 the client sees a connection reset. When the
// connection cannot be hijacked the handler is aborted instead, which makes
// the server close it.
func dropConnection(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}

	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}

	_ = conn.Close()
}
