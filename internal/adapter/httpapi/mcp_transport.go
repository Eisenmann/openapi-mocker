package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// MCP Streamable HTTP transport details.
const (
	headerMCPSession  = "Mcp-Session-Id"
	headerMCPProtocol = "MCP-Protocol-Version"
	// headerMockStream forces an SSE answer for clients that accept both
	// JSON and an event stream (which is what every MCP client sends).
	headerMockStream = "X-Mock-Stream"
	contentTypeSSE   = "text/event-stream"
	methodInitialize = "initialize"
	maxMCPSessions   = 1000
	mcpSessionTTL    = time.Hour
	sessionIDBytes   = 16
)

// mcpKeepAlive is how often an idle GET stream gets a comment line.
var mcpKeepAlive = 15 * time.Second //nolint:gochecknoglobals // overridden by tests.

// Errors answered by the MCP transport.
var (
	errMCPProtocolVersion = errors.New("unsupported MCP-Protocol-Version")
	errMCPSessionRequired = errors.New("Mcp-Session-Id header is required")
	errMCPSessionUnknown  = errors.New("unknown or expired MCP session")
	errMCPNeedsSSE        = errors.New("the Accept header must include text/event-stream")
)

// mcpSession is one client session started by an initialize request.
type mcpSession struct {
	project  string
	lastSeen time.Time
	done     chan struct{}
}

// mcpSessions keeps the sessions of the mock MCP endpoints in memory.
type mcpSessions struct {
	mu   sync.Mutex
	byID map[string]*mcpSession
	now  func() time.Time
}

func newMCPSessions() *mcpSessions {
	return &mcpSessions{mu: sync.Mutex{}, byID: map[string]*mcpSession{}, now: time.Now}
}

// start opens a session for a project and returns its id. Expired sessions
// are dropped, and the oldest one makes room when the store is full.
func (s *mcpSessions) start(project string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for id, sess := range s.byID {
		if now.Sub(sess.lastSeen) > mcpSessionTTL {
			s.drop(id)
		}
	}

	if len(s.byID) >= maxMCPSessions {
		oldest, oldestAt := "", now
		for id, sess := range s.byID {
			if oldest == "" || sess.lastSeen.Before(oldestAt) {
				oldest, oldestAt = id, sess.lastSeen
			}
		}

		s.drop(oldest)
	}

	buf := make([]byte, sessionIDBytes)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	s.byID[id] = &mcpSession{project: project, lastSeen: now, done: make(chan struct{})}

	return id
}

// get returns the live session with this id for the project and refreshes it.
func (s *mcpSessions) get(project, id string) (*mcpSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byID[id]
	if !ok || sess.project != project {
		return nil, false
	}

	if s.now().Sub(sess.lastSeen) > mcpSessionTTL {
		s.drop(id)

		return nil, false
	}

	sess.lastSeen = s.now()

	return sess, true
}

// end terminates a session; it reports whether the session existed.
func (s *mcpSessions) end(project, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byID[id]
	if !ok || sess.project != project {
		return false
	}

	s.drop(id)

	return true
}

// drop removes a session and closes its streams (the caller holds the lock).
func (s *mcpSessions) drop(id string) {
	if sess, ok := s.byID[id]; ok {
		close(sess.done)
		delete(s.byID, id)
	}
}

// checkTransport validates the MCP-Protocol-Version and Mcp-Session-Id
// headers of a request. It answers the error itself and returns nil, false
// when the request must not go on; sess is nil for a request without a
// session (sessions are optional unless the server requires them).
func (a *api) checkTransport(w http.ResponseWriter, r *http.Request, initialize bool) (*mcpSession, bool) {
	version := r.Header.Get(headerMCPProtocol)
	if version != "" && !slices.Contains(usecase.MCPSupportedProtocolVersions, version) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%w: %s", errMCPProtocolVersion, version))

		return nil, false
	}

	id := r.Header.Get(headerMCPSession)
	if id == "" {
		if a.s.MCPRequireSession && !initialize {
			writeError(w, http.StatusBadRequest, errMCPSessionRequired)

			return nil, false
		}

		return nil, true
	}

	sess, ok := a.sessions.get(r.PathValue("projectId"), id)
	if !ok {
		writeError(w, http.StatusNotFound, errMCPSessionUnknown)

		return nil, false
	}

	return sess, true
}

// rpcMethod returns the method of a single JSON-RPC request ("" otherwise).
func rpcMethod(body []byte) string {
	var r struct {
		Method string `json:"method"`
	}

	if json.Unmarshal(bytes.TrimSpace(body), &r) != nil {
		return ""
	}

	return r.Method
}

// hasResult reports whether a JSON-RPC response is a success.
func hasResult(resp []byte) bool {
	var r struct {
		Result json.RawMessage `json:"result"`
	}

	return json.Unmarshal(resp, &r) == nil && len(r.Result) > 0
}

// wantsSSE decides whether the answer to a POST is an event stream: the
// client accepts nothing but an event stream, or asks for one explicitly
// with the X-Mock-Stream: sse header.
func wantsSSE(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, contentTypeSSE) {
		return false
	}

	return !strings.Contains(accept, "application/json") || strings.EqualFold(r.Header.Get(headerMockStream), "sse")
}

// progressToken returns the _meta.progressToken of a tools/call request.
func progressToken(body []byte) json.RawMessage {
	var r struct {
		Method string `json:"method"`
		Params struct {
			Meta struct {
				Token json.RawMessage `json:"progressToken"`
			} `json:"_meta"`
		} `json:"params"`
	}

	if json.Unmarshal(bytes.TrimSpace(body), &r) != nil || r.Method != "tools/call" {
		return nil
	}

	return r.Params.Meta.Token
}

// writeSSE answers a request as an event stream: a progress notification
// pair when the client sent a progressToken, then one message event per
// JSON-RPC response (a batch answer is split into its elements).
func writeSSE(w http.ResponseWriter, status int, request, body []byte) {
	w.Header().Set("Content-Type", contentTypeSSE)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(status)

	var events [][]byte

	if token := progressToken(request); len(token) > 0 {
		const steps = 2

		for i := 1; i <= steps; i++ {
			note := fmt.Sprintf(
				`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%s,"progress":%d,"total":%d}}`,
				token, i, steps+1,
			)
			events = append(events, []byte(note))
		}
	}

	events = append(events, splitBatch(body)...)

	for i, ev := range events {
		_, _ = fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", i+1, ev)
	}

	flush(w)
}

// splitBatch returns the elements of a JSON array response, or the body.
func splitBatch(body []byte) [][]byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return [][]byte{trimmed}
	}

	var items []json.RawMessage
	if json.Unmarshal(trimmed, &items) != nil {
		return [][]byte{trimmed}
	}

	out := make([][]byte, 0, len(items))
	for _, it := range items {
		out = append(out, it)
	}

	return out
}

func flush(w http.ResponseWriter) {
	err := http.NewResponseController(w).Flush()
	_ = err
}

// serveMCPStream opens the server-to-client stream of the MCP endpoint
// (GET /mock/{projectId}/mcp). The mock has nothing to push, so the stream
// carries keep-alive comments until the client leaves or the session ends.
func (a *api) serveMCPStream(w http.ResponseWriter, r *http.Request) {
	sess, ok := a.openStream(w, r)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", contentTypeSSE)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The stream outlives the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	_, _ = fmt.Fprint(w, ": stream open\n\n")
	flush(w)

	var ended <-chan struct{}
	if sess != nil {
		ended = sess.done
	}

	ticker := time.NewTicker(mcpKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ended:
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flush(w)
		}
	}
}

// openStream runs the checks that precede a GET stream; it has answered the
// request itself when it returns false.
func (a *api) openStream(w http.ResponseWriter, r *http.Request) (*mcpSession, bool) {
	if a.s.MCPServing == nil {
		writeError(w, http.StatusNotFound, errMCPNotEnabled)
		return nil, false
	}

	if a.guardEndpoint(w, r) {
		return nil, false
	}

	sess, ok := a.checkTransport(w, r, false)
	if !ok {
		return nil, false
	}

	if !strings.Contains(r.Header.Get("Accept"), contentTypeSSE) {
		writeError(w, http.StatusNotAcceptable, errMCPNeedsSSE)
		return nil, false
	}

	_, err := a.s.MCPServing.Tools(r.PathValue("projectId"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return nil, false
	}

	return sess, true
}

// endMCPSession terminates a session (DELETE /mock/{projectId}/mcp).
func (a *api) endMCPSession(w http.ResponseWriter, r *http.Request) {
	if a.s.MCPServing == nil {
		writeError(w, http.StatusNotFound, errMCPNotEnabled)
		return
	}

	if a.guardEndpoint(w, r) {
		return
	}

	id := r.Header.Get(headerMCPSession)
	if id == "" {
		writeError(w, http.StatusBadRequest, errMCPSessionRequired)
		return
	}

	if !a.sessions.end(r.PathValue("projectId"), id) {
		writeError(w, http.StatusNotFound, errMCPSessionUnknown)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
