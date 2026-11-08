package httpapi_test

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const streamSpec = `openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /events:
    get:
      responses:
        '200':
          description: OK
  /chat:
    get:
      responses:
        '200':
          description: OK
`

func TestMock_ServerSentEvents(t *testing.T) {
	t.Parallel()

	rule := &domain.MockRule{
		Path: "/events", Method: http.MethodGet, StatusCode: http.StatusOK,
		Stream: &domain.StreamScript{
			RetryMs: 2500,
			Steps: []domain.StreamStep{
				{Event: "tick", Data: `{"n":{{counter}}}`, Repeat: 3, IntervalMs: 5},
				{Event: "done", Data: "bye\nnow", ID: "last"},
			},
		},
	}
	h, id := newTemplatingRouter(t, "yaml", streamSpec, rule)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/mock/" + id + "/events") //nolint:noctx // test server.
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}

	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	for _, want := range []string{"retry: 2500\n\n", "event: tick\ndata: {\"n\":", "id: last\n", "data: bye\ndata: now\n\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream lacks %q:\n%s", want, body)
		}
	}

	if got := strings.Count(body, "event: tick"); got != 3 {
		t.Errorf("want 3 tick events, got %d", got)
	}
}

func TestMock_ServerSentEvents_ClientDisconnect(t *testing.T) {
	t.Parallel()

	rule := &domain.MockRule{
		Path: "/events", Method: http.MethodGet, StatusCode: http.StatusOK,
		Stream: &domain.StreamScript{Loop: true, Steps: []domain.StreamStep{{Data: "x", IntervalMs: 20, Repeat: 1, DelayMs: 20}}},
	}
	h, id := newTemplatingRouter(t, "yaml", streamSpec, rule)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/mock/" + id + "/events") //nolint:noctx // test server.
	if err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: x") {
		t.Fatalf("first line %q, err %v", line, err)
	}

	resp.Body.Close() // the server must notice and stop (srv.Close would hang otherwise).
}

// wsClient is a minimal masked-frame client.
type wsClient struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func dialWS(t *testing.T, srvURL, path string) (*wsClient, *http.Response) {
	t.Helper()

	return dialWSProto(t, srvURL, path, "")
}

func dialWSProto(t *testing.T, srvURL, path, protocol string) (*wsClient, *http.Response) {
	t.Helper()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srvURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { conn.Close() })

	protoHeader := ""
	if protocol != "" {
		protoHeader = "Sec-WebSocket-Protocol: " + protocol + "\r\n"
	}

	keyRaw := make([]byte, 16)
	_, _ = rand.Read(keyRaw)
	key := base64.StdEncoding.EncodeToString(keyRaw)

	_, err = io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+key+"\r\n"+protoHeader+"\r\n")
	if err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}

	return &wsClient{t: t, c: conn, br: br}, resp
}

func (w *wsClient) send(op byte, payload string) {
	w.t.Helper()

	frame := []byte{0x80 | op}
	n := len(payload)

	switch {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	default:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	}

	mask := []byte{1, 2, 3, 4}
	frame = append(frame, mask...)

	for i := 0; i < n; i++ {
		frame = append(frame, payload[i]^mask[i%4])
	}

	_, err := w.c.Write(frame)
	if err != nil {
		w.t.Fatal(err)
	}
}

// read returns the next frame's opcode and payload.
func (w *wsClient) read() (op byte, payload string) {
	w.t.Helper()

	_ = w.c.SetReadDeadline(time.Now().Add(3 * time.Second))

	var hdr [2]byte

	_, err := io.ReadFull(w.br, hdr[:])
	if err != nil {
		w.t.Fatalf("read frame: %v", err)
	}

	n := int(hdr[1] & 0x7f)
	if n == 126 {
		var b [2]byte

		_, _ = io.ReadFull(w.br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	}

	buf := make([]byte, n)
	_, _ = io.ReadFull(w.br, buf)

	return hdr[0] & 0x0f, string(buf)
}

func TestMock_WebSocket(t *testing.T) {
	t.Parallel()

	rule := &domain.MockRule{
		Path: "/chat", Method: http.MethodGet, StatusCode: http.StatusOK,
		WebSocket: &domain.WSScript{
			OnConnect: []domain.StreamStep{{Data: "welcome"}},
			Rules: []domain.WSRule{
				{Match: domain.WSMatch{Type: domain.WSMatchEquals, Pattern: "ping"}, Reply: []domain.StreamStep{{Data: "pong"}}},
				{Match: domain.WSMatch{Type: domain.WSMatchContains, Pattern: "bye"}, Reply: []domain.StreamStep{{Data: "see you"}}, Close: true},
			},
			Fallback: domain.WSFallbackEcho,
		},
	}
	h, id := newTemplatingRouter(t, "yaml", streamSpec, rule)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, resp := dialWS(t, srv.URL, "/mock/"+id+"/chat")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status %d", resp.StatusCode)
	}

	// RFC 6455 section 1.3 sample key/accept pair is covered in the websocket package tests.
	if resp.Header.Get("Sec-WebSocket-Accept") == "" {
		t.Error("missing Sec-WebSocket-Accept")
	}

	if op, msg := c.read(); op != 1 || msg != "welcome" {
		t.Fatalf("on-connect frame: op %d %q", op, msg)
	}

	c.send(1, "ping")

	if _, msg := c.read(); msg != "pong" {
		t.Errorf("rule reply %q", msg)
	}

	c.send(1, "hello")

	if _, msg := c.read(); msg != "hello" {
		t.Errorf("echo fallback %q", msg)
	}

	c.send(9, "hb") // ping frame: the server answers with a pong.

	if op, msg := c.read(); op != 10 || msg != "hb" {
		t.Errorf("pong: op %d %q", op, msg)
	}

	c.send(1, "say bye")

	if _, msg := c.read(); msg != "see you" {
		t.Errorf("closing reply %q", msg)
	}

	if op, _ := c.read(); op != 8 {
		t.Errorf("want a close frame, got op %d", op)
	}
}

func TestMock_WebSocket_PlainRequestGets426(t *testing.T) {
	t.Parallel()

	rule := &domain.MockRule{
		Path: "/chat", Method: http.MethodGet, StatusCode: http.StatusOK,
		WebSocket: &domain.WSScript{Fallback: domain.WSFallbackEcho},
	}
	h, id := newTemplatingRouter(t, "yaml", streamSpec, rule)

	rec := call(t, h, http.MethodGet, "/mock/"+id+"/chat", "")
	if rec.Code != http.StatusUpgradeRequired || rec.Header().Get("Upgrade") != "websocket" {
		t.Errorf("want 426 with Upgrade header, got %d %v", rec.Code, rec.Header())
	}
}
