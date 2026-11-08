package websocket_test

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/websocket"
)

// sampleKey/sampleAccept come from RFC 6455, section 1.3.
const (
	sampleKey    = "dGhlIHNhbXBsZSBub25jZQ=="
	sampleAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
)

type result struct {
	msgs []string
	err  error
}

// echoServer echoes every message and reports what it read when the connection ends.
func echoServer(t *testing.T, opts websocket.Options) (addr string, done chan result) {
	t.Helper()

	done = make(chan result, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Upgrade(w, r, opts)
		if err != nil {
			return
		}

		var res result

		for {
			m, err := c.ReadMessage()
			if err != nil {
				res.err = err

				break
			}

			res.msgs = append(res.msgs, string(m))
			_ = c.WriteText(string(m))
		}

		done <- res
	}))
	t.Cleanup(srv.Close)

	return strings.TrimPrefix(srv.URL, "http://"), done
}

func handshake(t *testing.T, addr, extra string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { conn.Close() })

	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+sampleKey+"\r\n"+extra+"\r\n")

	br := bufio.NewReader(conn)

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}

	return conn, br, resp
}

func frame(fin bool, op byte, payload []byte, masked bool) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}

	out := []byte{b0}
	n := len(payload)

	var mbit byte
	if masked {
		mbit = 0x80
	}

	switch {
	case n < 126:
		out = append(out, mbit|byte(n))
	case n < 1<<16:
		out = append(out, mbit|126, byte(n>>8), byte(n))
	default:
		out = append(out, mbit|127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}

	if !masked {
		return append(out, payload...)
	}

	mask := []byte{9, 8, 7, 6}
	out = append(out, mask...)

	for i, c := range payload {
		out = append(out, c^mask[i%4])
	}

	return out
}

func readFrame(t *testing.T, conn net.Conn, br *bufio.Reader) (op byte, payload []byte) {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		t.Fatalf("read: %v", err)
	}

	n := int(h[1] & 0x7f)

	switch n {
	case 126:
		var b [2]byte
		_, _ = io.ReadFull(br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		_, _ = io.ReadFull(br, b[:])
		n = int(binary.BigEndian.Uint64(b[:]))
	}

	payload = make([]byte, n)
	_, _ = io.ReadFull(br, payload)

	return h[0] & 0x0f, payload
}

func TestHandshake(t *testing.T) {
	t.Parallel()

	addr, _ := echoServer(t, websocket.Options{Subprotocols: []string{"chat", "other"}})
	_, _, resp := handshake(t, addr, "Sec-WebSocket-Protocol: nope, other\r\n")

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}

	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != sampleAccept {
		t.Errorf("accept %q, want %q", got, sampleAccept)
	}

	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != "other" {
		t.Errorf("subprotocol %q", got)
	}
}

func TestBadHandshake(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := websocket.Upgrade(w, r, websocket.Options{})
		if err == nil {
			t.Error("expected an error")
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx // test server.
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestFragmentsPingAndClose(t *testing.T) {
	t.Parallel()

	addr, done := echoServer(t, websocket.Options{})
	conn, br, _ := handshake(t, addr, "")

	// A fragmented text message with a ping in the middle.
	_, _ = conn.Write(frame(false, 1, []byte("hel"), true))
	_, _ = conn.Write(frame(true, 9, []byte("p"), true))
	_, _ = conn.Write(frame(true, 0, []byte("lo"), true))

	if op, p := readFrame(t, conn, br); op != 10 || string(p) != "p" {
		t.Errorf("want pong p, got %d %q", op, p)
	}

	if op, p := readFrame(t, conn, br); op != 1 || string(p) != "hello" {
		t.Errorf("want text hello, got %d %q", op, p)
	}

	// A message that needs the 16-bit length.
	big := strings.Repeat("a", 300)
	_, _ = conn.Write(frame(true, 1, []byte(big), true))

	if _, p := readFrame(t, conn, br); string(p) != big {
		t.Errorf("long echo has %d bytes", len(p))
	}

	_, _ = conn.Write(frame(true, 8, []byte{0x03, 0xe8}, true))

	if op, p := readFrame(t, conn, br); op != 8 || len(p) < 2 || binary.BigEndian.Uint16(p) != 1000 {
		t.Errorf("want close 1000, got %d %v", op, p)
	}

	res := <-done
	if !errors.Is(res.err, websocket.ErrClosed) || len(res.msgs) != 2 {
		t.Errorf("server result %+v", res)
	}
}

func TestProtocolViolations(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		frame []byte
		code  uint16
	}{
		"unmasked frame":      {frame(true, 1, []byte("x"), false), 1002},
		"bad utf8":            {frame(true, 1, []byte{0xff, 0xfe}, true), 1007},
		"orphan continuation": {frame(true, 0, []byte("x"), true), 1002},
		"fragmented control":  {frame(false, 9, []byte("x"), true), 1002},
		"too big":             {frame(true, 2, make([]byte, 2000), true), 1009},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			addr, _ := echoServer(t, websocket.Options{MaxMessage: 1000})
			conn, br, _ := handshake(t, addr, "")
			_, _ = conn.Write(tc.frame)

			op, p := readFrame(t, conn, br)
			if op != 8 || len(p) < 2 || binary.BigEndian.Uint16(p) != tc.code {
				t.Errorf("want close %d, got op %d %v", tc.code, op, p)
			}
		})
	}
}
