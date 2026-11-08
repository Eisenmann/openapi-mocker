package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errBoom = errors.New("boom")

func gqlSetup(t *testing.T) (srvURL, projectID string, eng *stubGraphQLEngine) {
	t.Helper()

	h, _, contracts, _, _, _, _, _, _, gqlEngine := newTestRouter(t)

	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p struct{ ID string }
	decodeJSON(t, rec, &p)

	contracts.AddVersion(p.ID, "graphql", "type Query { ping: String } type Subscription { tick: Int }", "manual")
	gqlEngine.executeBody = []byte(`{"data":{"tick":1}}`)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv.URL, p.ID, gqlEngine
}

// dialGQL opens a graphql-transport-ws connection.
func dialGQL(t *testing.T, srvURL, path, protocol string) (*wsClient, *http.Response) {
	t.Helper()

	return dialWSProto(t, srvURL, path, protocol)
}

func (w *wsClient) sendJSON(v string) { w.send(1, v) }

func (w *wsClient) readMsg() map[string]any {
	w.t.Helper()

	op, p := w.read()
	if op != 1 {
		w.t.Fatalf("want a text frame, got op %d %q", op, p)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(p), &m); err != nil {
		w.t.Fatalf("bad message %q: %v", p, err)
	}

	return m
}

func TestGraphQLWS_Subscription(t *testing.T) {
	t.Parallel()

	url, id, _ := gqlSetup(t)
	c, resp := dialGQL(t, url, "/mock/"+id+"/graphql?events=3&intervalMs=1", "graphql-transport-ws")

	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Protocol") != "graphql-transport-ws" {
		t.Fatalf("handshake: %d %v", resp.StatusCode, resp.Header)
	}

	c.sendJSON(`{"type":"connection_init"}`)

	if m := c.readMsg(); m["type"] != "connection_ack" {
		t.Fatalf("want ack, got %v", m)
	}

	c.sendJSON(`{"type":"ping"}`)

	if m := c.readMsg(); m["type"] != "pong" {
		t.Fatalf("want pong, got %v", m)
	}

	c.sendJSON(`{"id":"s1","type":"subscribe","payload":{"query":"subscription { tick }"}}`)

	for i := 0; i < 3; i++ {
		m := c.readMsg()
		if m["type"] != "next" || m["id"] != "s1" {
			t.Fatalf("event %d: %v", i, m)
		}

		if data, _ := json.Marshal(m["payload"]); string(data) != `{"data":{"tick":1}}` {
			t.Errorf("payload %s", data)
		}
	}

	if m := c.readMsg(); m["type"] != "complete" || m["id"] != "s1" {
		t.Fatalf("want complete, got %v", m)
	}

	// A query yields a single result and completes.
	c.sendJSON(`{"id":"q1","type":"subscribe","payload":{"query":"query Q { ping }","operationName":"Q"}}`)

	if m := c.readMsg(); m["type"] != "next" {
		t.Fatalf("query next: %v", m)
	}

	if m := c.readMsg(); m["type"] != "complete" {
		t.Fatalf("query complete: %v", m)
	}
}

func TestGraphQLWS_ClientCompleteStopsStream(t *testing.T) {
	t.Parallel()

	url, id, _ := gqlSetup(t)
	c, _ := dialGQL(t, url, "/mock/"+id+"/graphql?events=1000&intervalMs=20", "graphql-transport-ws")

	c.sendJSON(`{"type":"connection_init"}`)
	c.readMsg()
	c.sendJSON(`{"id":"s","type":"subscribe","payload":{"query":"subscription { tick }"}}`)

	if m := c.readMsg(); m["type"] != "next" {
		t.Fatalf("%v", m)
	}

	c.sendJSON(`{"id":"s","type":"complete"}`)
	c.sendJSON(`{"type":"ping"}`)

	// Drain in-flight events until the pong arrives; no complete is sent for a client-completed id.
	for i := 0; i < 5; i++ {
		m := c.readMsg()
		if m["type"] == "pong" {
			return
		}

		if m["type"] == "complete" {
			t.Fatal("server must not send complete after the client completed")
		}
	}

	t.Fatal("pong not received")
}

func TestGraphQLWS_ProtocolErrors(t *testing.T) {
	t.Parallel()

	url, id, eng := gqlSetup(t)

	closeCode := func(c *wsClient) uint16 {
		t.Helper()

		op, p := c.read()
		if op != 8 || len(p) < 2 {
			t.Fatalf("want close frame, got op %d %v", op, p)
		}

		return uint16(p[0])<<8 | uint16(p[1])
	}

	// No subprotocol.
	c, _ := dialGQL(t, url, "/mock/"+id+"/graphql", "")
	if code := closeCode(c); code != 4406 {
		t.Errorf("no subprotocol: close %d", code)
	}

	// Subscribe before the connection is acknowledged.
	c, _ = dialGQL(t, url, "/mock/"+id+"/graphql", "graphql-transport-ws")
	c.sendJSON(`{"id":"a","type":"subscribe","payload":{"query":"{ ping }"}}`)

	if code := closeCode(c); code != 4401 {
		t.Errorf("unauthorized: close %d", code)
	}

	// Second connection_init.
	c, _ = dialGQL(t, url, "/mock/"+id+"/graphql", "graphql-transport-ws")
	c.sendJSON(`{"type":"connection_init"}`)
	c.readMsg()
	c.sendJSON(`{"type":"connection_init"}`)

	if code := closeCode(c); code != 4429 {
		t.Errorf("double init: close %d", code)
	}

	// Garbage.
	c, _ = dialGQL(t, url, "/mock/"+id+"/graphql", "graphql-transport-ws")
	c.sendJSON(`not json`)

	if code := closeCode(c); code != 4400 {
		t.Errorf("garbage: close %d", code)
	}

	// An engine failure becomes an error message.
	eng.executeErr = errBoom
	c, _ = dialGQL(t, url, "/mock/"+id+"/graphql", "graphql-transport-ws")
	c.sendJSON(`{"type":"connection_init"}`)
	c.readMsg()
	c.sendJSON(`{"id":"e","type":"subscribe","payload":{"query":"{ ping }"}}`)

	if m := c.readMsg(); m["type"] != "error" || m["id"] != "e" {
		t.Errorf("want error message, got %v", m)
	}
}

func TestGraphQLWS_PlainGetIs426(t *testing.T) {
	t.Parallel()

	url, id, _ := gqlSetup(t)

	resp, err := http.Get(url + "/mock/" + id + "/graphql") //nolint:noctx // test server.
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status %d", resp.StatusCode)
	}
}

// OperationType lets the stub engine tell subscriptions from other operations.
func (m *stubGraphQLEngine) OperationType(query, _ string) (string, error) {
	if strings.Contains(query, "subscription") {
		return "subscription", nil
	}

	return "query", nil
}

func TestGraphQLWS_LegacyProtocol(t *testing.T) {
	t.Parallel()

	url, id, eng := gqlSetup(t)
	c, resp := dialGQL(t, url, "/mock/"+id+"/graphql?events=2&intervalMs=1", "graphql-ws")

	if resp.Header.Get("Sec-WebSocket-Protocol") != "graphql-ws" {
		t.Fatalf("negotiated %q", resp.Header.Get("Sec-WebSocket-Protocol"))
	}

	c.sendJSON(`{"type":"connection_init","payload":{}}`)

	if m := c.readMsg(); m["type"] != "connection_ack" {
		t.Fatalf("want ack, got %v", m)
	}

	if m := c.readMsg(); m["type"] != "ka" {
		t.Fatalf("want keep-alive, got %v", m)
	}

	c.sendJSON(`{"id":"1","type":"start","payload":{"query":"subscription { tick }"}}`)

	for i := 0; i < 2; i++ {
		m := c.readMsg()
		if m["type"] != "data" || m["id"] != "1" {
			t.Fatalf("event %d: %v", i, m)
		}
	}

	if m := c.readMsg(); m["type"] != "complete" || m["id"] != "1" {
		t.Fatalf("want complete, got %v", m)
	}

	// A failing operation is a single error object.
	eng.executeErr = errBoom

	c.sendJSON(`{"id":"2","type":"start","payload":{"query":"{ ping }"}}`)

	m := c.readMsg()
	if _, isObject := m["payload"].(map[string]any); m["type"] != "error" || !isObject {
		t.Errorf("want an error with an object payload, got %v", m)
	}

	// stop ends a stream; connection_terminate closes the socket.
	eng.executeErr = nil

	c.sendJSON(`{"id":"3","type":"start","payload":{"query":"subscription { tick }"}}`)
	c.readMsg()
	c.sendJSON(`{"id":"3","type":"stop"}`)
	c.sendJSON(`{"type":"connection_terminate"}`)

	for i := 0; i < 20; i++ {
		if op, _ := c.read(); op == 8 {
			return
		}
	}

	t.Error("the socket was not closed after connection_terminate")
}
