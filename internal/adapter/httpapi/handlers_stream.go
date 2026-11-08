package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/websocket"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// serveEventStream plays a Server-Sent Events plan: it keeps the response open,
// flushes every event as it is produced and ends when the script is done or
// the client goes away.
func serveEventStream(w http.ResponseWriter, r *http.Request, resp *usecase.MockResponse) {
	rc := http.NewResponseController(w)
	// The server-wide write timeout would cut long streams.
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", usecase.ContentTypeEventStream)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	if ms := resp.Stream.RetryMs(); ms > 0 {
		_, _ = fmt.Fprintf(w, "retry: %s\n\n", strconv.Itoa(ms))
	}

	_ = rc.Flush()

	_ = resp.Stream.Play(r.Context(), func(ev usecase.StreamEvent) error {
		_, err := w.Write([]byte(ev.Format()))
		if err != nil {
			return err
		}

		return rc.Flush()
	})
}

// serveWebSocket upgrades the request and runs the plan on the connection.
// A request that is not an upgrade gets 426.
func serveWebSocket(w http.ResponseWriter, r *http.Request, plan *usecase.WSPlan) {
	if !usecase.IsWebSocketUpgrade(r.Header) {
		w.Header().Set("Upgrade", "websocket")
		http.Error(w, "this endpoint speaks WebSocket; send an Upgrade request", http.StatusUpgradeRequired)

		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	conn, err := websocket.Upgrade(w, r, websocket.Options{Subprotocols: requestedSubprotocols(r), MaxMessage: 0})
	if err != nil {
		return // Upgrade has answered the client.
	}

	defer conn.Close(websocket.CloseNormal, "")

	plan.Run(r.Context(), conn)
}

// requestedSubprotocols accepts whatever the client offers first: the mock
// has no protocol of its own, so any choice is as good as another.
func requestedSubprotocols(r *http.Request) []string {
	var out []string

	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			out = append(out, strings.TrimSpace(p))
		}
	}

	return out
}
