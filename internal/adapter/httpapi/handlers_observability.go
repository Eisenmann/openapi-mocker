package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	// streamHeartbeat keeps idle streams alive through proxies.
	streamHeartbeat = 15 * time.Second
	// maxLogLimit caps ?limit= on the log list.
	maxLogLimit = 1000
	// headerLastEventID is sent by an SSE client that reconnects.
	headerLastEventID = "Last-Event-ID"
	// envMetricsToken names the variable holding the bearer token of /metrics.
	envMetricsToken = "METRICS_TOKEN"
)

func (a *api) registerObservabilityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/logs/stream", a.streamLogs)
	mux.HandleFunc("GET /api/projects/{id}/logs/{logId}", a.getLog)
	mux.HandleFunc("GET /api/projects/{id}/stats", a.getStats)
	mux.HandleFunc("DELETE /api/projects/{id}/stats", a.resetStats)
	mux.HandleFunc("GET /metrics", a.serveMetrics)
	a.registerVerifyRoutes(mux)
}

// listLogs returns the most recent request log entries, newest first, without
// their captured bodies (see getLog). ?limit= caps the number.
func (a *api) listLogs(w http.ResponseWriter, r *http.Request) {
	limit := defaultLogLimit

	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errInvalidLimit)
			return
		}

		limit = min(n, maxLogLimit)
	}

	writeJSON(w, http.StatusOK, a.s.Logs.List(r.PathValue("id"), limit))
}

// getLog returns one entry with its captured request and response.
func (a *api) getLog(w http.ResponseWriter, r *http.Request) {
	l, err := a.s.Logs.Detail(r.PathValue("id"), r.PathValue("logId"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, l)
}

type statsResponse struct {
	usecase.ProjectStats

	Capture captureInfo     `json:"capture"`
	Logs    usecase.LogInfo `json:"logs"`
}

type captureInfo struct {
	Enabled  bool `json:"enabled"`
	MaxBytes int  `json:"maxBytes"`
}

func (a *api) getStats(w http.ResponseWriter, r *http.Request) {
	st, err := a.s.Logs.Stats(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	p := a.s.Logs.CapturePolicy()

	writeJSON(w, http.StatusOK, statsResponse{
		ProjectStats: st, Capture: captureInfo{Enabled: p.Enabled, MaxBytes: p.MaxBytes}, Logs: a.s.Logs.Info(),
	})
}

func (a *api) resetStats(w http.ResponseWriter, r *http.Request) {
	err := a.s.Logs.ResetStats(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// streamLogs pushes new request-log entries of a project as Server-Sent
// Events. Each entry is an event named "log" whose id is the entry id, so a
// client that reconnects with Last-Event-ID receives what it missed. An event
// named "lag" reports entries dropped because the client read too slowly.
func (a *api) streamLogs(w http.ResponseWriter, r *http.Request) {
	afterID := r.Header.Get(headerLastEventID)
	if afterID == "" {
		afterID = r.URL.Query().Get("after")
	}

	sub, backlog, err := a.s.Logs.Subscribe(r.PathValue("id"), afterID)
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}
	defer sub.Close()

	rc := http.NewResponseController(w)
	// A stream outlives the server's write timeout.
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if !writeStreamFrame(w, rc, ": connected\nretry: 3000\n\n") {
		return
	}

	for _, l := range backlog {
		if !writeLogEvent(w, rc, l) {
			return
		}
	}

	pumpStream(r.Context(), w, rc, sub)
}

// pumpStream forwards entries and heartbeats until the client goes away.
func pumpStream(ctx context.Context, w http.ResponseWriter, rc *http.ResponseController, sub *usecase.Subscription) {
	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case l, open := <-sub.Entries():
			if !open || !writeLagEvent(w, rc, sub) || !writeLogEvent(w, rc, l) {
				return
			}
		case <-ticker.C:
			if !writeLagEvent(w, rc, sub) || !writeStreamFrame(w, rc, ": ping\n\n") {
				return
			}
		}
	}
}

func writeLogEvent(w http.ResponseWriter, rc *http.ResponseController, l *domain.RequestLog) bool {
	data, err := json.Marshal(l)
	if err != nil {
		return true
	}

	return writeStreamFrame(w, rc, "id: "+l.ID+"\nevent: log\ndata: "+string(data)+"\n\n")
}

func writeLagEvent(w http.ResponseWriter, rc *http.ResponseController, sub *usecase.Subscription) bool {
	n := sub.TakeDropped()
	if n == 0 {
		return true
	}

	return writeStreamFrame(w, rc, "event: lag\ndata: {\"dropped\":"+strconv.FormatInt(n, 10)+"}\n\n")
}

func writeStreamFrame(w http.ResponseWriter, rc *http.ResponseController, frame string) bool {
	_, err := w.Write([]byte(frame))
	if err != nil {
		return false
	}

	return rc.Flush() == nil
}

// serveMetrics exposes the Prometheus metrics. When METRICS_TOKEN is set the
// request must carry it as a bearer token.
func (a *api) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if token := os.Getenv(envMetricsToken); token != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !constantTimeEqual(got, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			writeError(w, http.StatusUnauthorized, errMetricsToken)

			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	err := a.s.Logs.WriteMetrics(w)
	if err != nil {
		w.Header().Del("Content-Type")
		writeError(w, usecase.StatusFromError(err), err)
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
