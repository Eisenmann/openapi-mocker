package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/websocket"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// GraphQL over WebSocket: the "graphql-transport-ws" protocol
// (https://github.com/enisdenjo/graphql-ws/blob/master/PROTOCOL.md) and the
// legacy "graphql-ws" protocol of subscriptions-transport-ws, which Apollo
// Client 2/3 and older tools still speak.
const (
	gqlWSProtocol       = "graphql-transport-ws"
	gqlWSLegacyProtocol = "graphql-ws"

	// Message types of the legacy protocol that differ from the new one.
	gqlLegacyStart     = "start"
	gqlLegacyStop      = "stop"
	gqlLegacyData      = "data"
	gqlLegacyKeepAlive = "ka"
	gqlLegacyTerminate = "connection_terminate"

	gqlMsgInit      = "connection_init"
	gqlMsgAck       = "connection_ack"
	gqlMsgPing      = "ping"
	gqlMsgPong      = "pong"
	gqlMsgSubscribe = "subscribe"
	gqlMsgNext      = "next"
	gqlMsgError     = "error"
	gqlMsgComplete  = "complete"

	// Application close codes defined by the protocol.
	gqlCloseBadRequest      = 4400
	gqlCloseUnauthorized    = 4401
	gqlCloseSubprotocol     = 4406
	gqlCloseAlreadyAcked    = 4429
	gqlCloseDuplicateSubID  = 4409
	defaultSubscriptionEvts = 5
	defaultSubscriptionMs   = 1000
	maxSubscriptionEvts     = 10000
	maxSubscriptionMs       = 60000
)

// gqlWSMessage is one protocol message.
type gqlWSMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type gqlSubscribePayload struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// serveGraphQLWS upgrades GET /mock/{id}/graphql and speaks graphql-transport-ws.
// A subscription emits `events` results spaced `intervalMs` apart (query
// parameters of the connection URL, 5 and 1000 ms by default); a query or
// mutation yields one result.
func (a *api) serveGraphQLWS(w http.ResponseWriter, r *http.Request) {
	if a.s.GraphQLServing == nil {
		writeError(w, http.StatusNotFound, errGraphQLNotEnabled)

		return
	}

	if a.guardEndpoint(w, r) {
		return
	}

	if !usecase.IsWebSocketUpgrade(r.Header) {
		w.Header().Set("Upgrade", "websocket")
		writeError(w, http.StatusUpgradeRequired, errGraphQLWSUpgrade)

		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	conn, err := websocket.Upgrade(w, r, websocket.Options{
		Subprotocols: []string{gqlWSProtocol, gqlWSLegacyProtocol}, MaxMessage: 0,
	})
	if err != nil {
		return
	}

	defer conn.Close(websocket.CloseNormal, "")

	if conn.Subprotocol != gqlWSProtocol && conn.Subprotocol != gqlWSLegacyProtocol {
		_ = conn.Close(gqlCloseSubprotocol, "Subprotocol not acceptable")

		return
	}

	s := &gqlSession{ //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
		a: a, conn: conn, projectID: r.PathValue("projectId"), legacy: conn.Subprotocol == gqlWSLegacyProtocol,
		events:   queryInt(r, "events", defaultSubscriptionEvts, maxSubscriptionEvts),
		interval: queryInt(r, "intervalMs", defaultSubscriptionMs, maxSubscriptionMs),
		subs:     map[string]context.CancelFunc{},
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.run(ctx)
	s.wg.Wait()
}

// queryInt reads a bounded non-negative integer query parameter.
func queryInt(r *http.Request, name string, def, maxVal int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}

	return min(n, maxVal)
}

type gqlSession struct {
	a         *api
	conn      *websocket.Conn
	projectID string
	legacy    bool // the subscriptions-transport-ws dialect.
	events    int
	interval  int

	mu    sync.Mutex
	acked bool
	subs  map[string]context.CancelFunc
	wg    sync.WaitGroup
}

func (s *gqlSession) send(m *gqlWSMessage) error {
	if s.legacy {
		m = legacyOutgoing(m)
		if m == nil {
			return nil
		}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return err
	}

	return s.conn.WriteText(string(b))
}

func (s *gqlSession) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()

		for _, cancel := range s.subs {
			cancel()
		}

		s.mu.Unlock()
	}()

	for {
		raw, err := s.conn.ReadMessage()
		if err != nil {
			return
		}

		var m gqlWSMessage

		if json.Unmarshal(raw, &m) != nil {
			_ = s.conn.Close(gqlCloseBadRequest, "invalid message")

			return
		}

		if !s.handle(ctx, &m) {
			return
		}
	}
}

// legacyOutgoing renames a server message for the legacy protocol; nil means
// the message does not exist there (pong).
func legacyOutgoing(m *gqlWSMessage) *gqlWSMessage {
	out := *m

	switch m.Type {
	case gqlMsgNext:
		out.Type = gqlLegacyData
	case gqlMsgPong:
		return nil
	case gqlMsgError:
		// The legacy error payload is one error object, not a list.
		var list []json.RawMessage

		if json.Unmarshal(m.Payload, &list) == nil && len(list) > 0 {
			out.Payload = list[0]
		}
	}

	return &out
}

// legacyIncoming maps a legacy client message onto the new protocol's names.
func legacyIncoming(m *gqlWSMessage) {
	switch m.Type {
	case gqlLegacyStart:
		m.Type = gqlMsgSubscribe
	case gqlLegacyStop:
		m.Type = gqlMsgComplete
	}
}

// handle processes one message; false ends the session.
func (s *gqlSession) handle(ctx context.Context, m *gqlWSMessage) bool {
	if s.legacy {
		legacyIncoming(m)

		if m.Type == gqlLegacyTerminate {
			return false
		}
	}

	switch m.Type {
	case gqlMsgInit:
		return s.handleInit()
	case gqlMsgPing:
		return s.send(&gqlWSMessage{Type: gqlMsgPong}) == nil //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
	case gqlMsgPong:
		return true
	case gqlMsgSubscribe:
		return s.subscribe(ctx, m)
	case gqlMsgComplete:
		s.cancelSubscription(m.ID)

		return true
	default:
		_ = s.conn.Close(gqlCloseBadRequest, "unknown message type")

		return false
	}
}

// handleInit acknowledges connection_init; false ends the session.
func (s *gqlSession) handleInit() bool {
	s.mu.Lock()
	already := s.acked
	s.acked = true
	s.mu.Unlock()

	if already && !s.legacy {
		_ = s.conn.Close(gqlCloseAlreadyAcked, "Too many initialisation requests")

		return false
	}

	if s.send(&gqlWSMessage{Type: gqlMsgAck}) != nil { //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
		return false
	}

	if s.legacy {
		return s.conn.WriteText(`{"type":"`+gqlLegacyKeepAlive+`"}`) == nil
	}

	return true
}

// cancelSubscription stops the subscription with the given id, if it runs.
func (s *gqlSession) cancelSubscription(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cancel, ok := s.subs[id]; ok {
		cancel()
		delete(s.subs, id)
	}
}

func (s *gqlSession) subscribe(ctx context.Context, m *gqlWSMessage) bool {
	s.mu.Lock()
	acked := s.acked
	_, dup := s.subs[m.ID]
	s.mu.Unlock()

	switch {
	case !acked:
		_ = s.conn.Close(gqlCloseUnauthorized, "Unauthorized")

		return false
	case dup || m.ID == "":
		_ = s.conn.Close(gqlCloseDuplicateSubID, "Subscriber for "+m.ID+" already exists")

		return false
	}

	var p gqlSubscribePayload

	if json.Unmarshal(m.Payload, &p) != nil || p.Query == "" {
		_ = s.conn.Close(gqlCloseBadRequest, "invalid subscribe payload")

		return false
	}

	subCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.subs[m.ID] = cancel
	s.mu.Unlock()

	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		defer cancel()

		s.stream(subCtx, m.ID, &p)

		s.mu.Lock()
		delete(s.subs, m.ID)
		s.mu.Unlock()
	}()

	return true
}

// stream emits the results of one operation and completes it.
func (s *gqlSession) stream(ctx context.Context, id string, p *gqlSubscribePayload) {
	count := 1
	if s.a.s.GraphQLServing.IsSubscription(p.Query, p.OperationName) {
		count = s.events
	}

	for i := range count {
		if i > 0 && !sleepContext(ctx, s.interval) {
			return
		}

		body, err := s.a.s.GraphQLServing.Serve(s.projectID, p.Query, p.OperationName, p.Variables)
		if err != nil {
			s.sendError(id, err)

			return
		}

		if s.send(&gqlWSMessage{ID: id, Type: gqlMsgNext, Payload: body}) != nil {
			return
		}
	}

	if ctx.Err() == nil {
		_ = s.send(&gqlWSMessage{ID: id, Type: gqlMsgComplete}) //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
	}
}

// sendError reports a failed operation: document errors as the protocol's
// `error` message, anything else as a single GraphQL error.
func (s *gqlSession) sendError(id string, err error) {
	var items any

	var qe *usecase.GraphQLQueryError

	if errors.As(err, &qe) {
		items = qe.Errors
	} else {
		items = []map[string]string{{"message": err.Error()}}
	}

	payload, _ := json.Marshal(items) //nolint:errchkjson // strings always marshal
	_ = s.send(&gqlWSMessage{ID: id, Type: gqlMsgError, Payload: payload})
}
