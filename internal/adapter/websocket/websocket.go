// Package websocket is a small WebSocket (RFC 6455) server built on the
// standard library: the opening handshake, framing, fragmentation, ping/pong
// and the closing handshake. It serves the mock endpoints; it is not a general
// purpose client library.
package websocket

import (
	"bufio"
	"crypto/sha1" //nolint:gosec // the handshake of RFC 6455 mandates SHA-1; it is not used for security.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Opcodes of RFC 6455.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Close codes of RFC 6455.
const (
	CloseNormal          = 1000
	CloseProtocolError   = 1002
	CloseInvalidPayload  = 1007
	ClosePolicyViolation = 1008
	CloseTooBig          = 1009
)

const (
	handshakeGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	// DefaultMaxMessage is the largest message accepted from a client.
	DefaultMaxMessage = 1 << 20
	// maxControlPayload is the limit for control frames (RFC 6455, 5.5).
	maxControlPayload = 125
	// writeTimeout bounds a single frame write, so a stuck client cannot hold a goroutine forever.
	writeTimeout = 15 * time.Second

	finBit      = 0x80
	maskBit     = 0x80
	opcodeMask  = 0x0F
	lenMask     = 0x7F
	len16       = 126
	len64       = 127
	maxUint16   = 1<<16 - 1
	closeHdrLen = 2
	// reservedBits are the RSV1-3 bits of the first header byte.
	reservedBits = 0x70
	// maxHeaderLen is the longest server frame header (2 + 8 bytes).
	maxHeaderLen = 10
)

// Errors returned by Upgrade and ReadMessage.
var (
	ErrNotWebSocket = errors.New("not a WebSocket upgrade request")
	ErrBadHandshake = errors.New("bad WebSocket handshake")
	ErrClosed       = errors.New("websocket closed")
)

// Options configure Upgrade.
type Options struct {
	// Subprotocols lists the subprotocols the server speaks, in order of
	// preference; the first one the client offers is chosen.
	Subprotocols []string
	// MaxMessage is the largest accepted message (default DefaultMaxMessage).
	MaxMessage int
}

// Conn is an upgraded connection.
type Conn struct {
	conn       net.Conn
	br         *bufio.Reader
	maxMessage int
	// Subprotocol is the negotiated subprotocol ("" if none).
	Subprotocol string

	wmu      sync.Mutex
	closed   bool
	closeErr error
}

// Upgrade completes the handshake and takes over the connection. On error it
// has already answered the client.
func Upgrade(w http.ResponseWriter, r *http.Request, opts Options) (*Conn, error) {
	err := checkHandshake(r)
	if err != nil {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, err.Error(), http.StatusBadRequest)

		return nil, err
	}

	proto := negotiate(r, opts.Subprotocols)

	rc := http.NewResponseController(w)

	conn, brw, err := rc.Hijack()
	if err != nil {
		http.Error(w, "websocket upgrade is not supported by this server", http.StatusInternalServerError)

		return nil, fmt.Errorf("%w: %w", ErrBadHandshake, err)
	}

	var resp strings.Builder

	resp.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n")
	resp.WriteString("Sec-WebSocket-Accept: " + acceptKey(r.Header.Get("Sec-WebSocket-Key")) + "\r\n")

	if proto != "" {
		resp.WriteString("Sec-WebSocket-Protocol: " + proto + "\r\n")
	}

	resp.WriteString("\r\n")

	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	_, err = conn.Write([]byte(resp.String()))
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	_ = conn.SetWriteDeadline(time.Time{})

	maxMsg := opts.MaxMessage
	if maxMsg <= 0 {
		maxMsg = DefaultMaxMessage
	}

	return &Conn{
		conn: conn, br: brw.Reader, maxMessage: maxMsg, Subprotocol: proto,
		wmu: sync.Mutex{}, closed: false, closeErr: nil,
	}, nil
}

func checkHandshake(r *http.Request) error {
	switch {
	case r.Method != http.MethodGet:
		return fmt.Errorf("%w: method must be GET", ErrNotWebSocket)
	case !headerHas(r.Header, "Connection", "upgrade") || !headerHas(r.Header, "Upgrade", "websocket"):
		return fmt.Errorf("%w: missing Upgrade headers", ErrNotWebSocket)
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		return fmt.Errorf("%w: Sec-WebSocket-Version must be 13", ErrBadHandshake)
	}

	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		return fmt.Errorf("%w: invalid Sec-WebSocket-Key", ErrBadHandshake)
	}

	return nil
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}

	return false
}

func negotiate(r *http.Request, supported []string) string {
	var offered []string

	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			offered = append(offered, strings.TrimSpace(p))
		}
	}

	for _, want := range supported {
		for _, got := range offered {
			if got == want {
				return want
			}
		}
	}

	return ""
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + handshakeGUID)) //nolint:gosec // see the import.

	return base64.StdEncoding.EncodeToString(sum[:])
}

// ReadMessage returns the next text or binary message, answering pings and
// the closing handshake on the way. It returns ErrClosed (or a network error)
// once the connection is over.
func (c *Conn) ReadMessage() ([]byte, error) {
	msg, _, err := c.readData()

	return msg, err
}

// WriteText sends a text message.
func (c *Conn) WriteText(data string) error { return c.writeFrame(opText, []byte(data)) }

// WriteBinary sends a binary message.
func (c *Conn) WriteBinary(data []byte) error { return c.writeFrame(opBinary, data) }

// Ping sends a ping frame.
func (c *Conn) Ping() error { return c.writeFrame(opPing, nil) }

// Close sends a close frame (once) and closes the connection.
func (c *Conn) Close(code int, reason string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	if c.closed {
		return c.closeErr
	}

	c.closed = true

	payload := make([]byte, closeHdrLen, closeHdrLen+len(reason))
	//nolint:gosec // close codes are small constants or echoed 16-bit values.
	binary.BigEndian.PutUint16(payload, uint16(code))
	payload = append(payload, truncateReason(reason)...)

	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, c.closeErr = c.conn.Write(encodeFrame(opClose, payload))
	_ = c.conn.Close()

	return c.closeErr
}

// readData reads one data message and reports whether it was text.
//
//nolint:cyclop // one loop handling fragmentation and control frames.
func (c *Conn) readData() (data []byte, isText bool, err error) {
	var buf []byte

	started := false

	for {
		fin, op, payload, ferr := c.readFrame()
		if ferr != nil {
			return nil, false, ferr
		}

		handled, cerr := c.control(op, payload)
		if cerr != nil {
			return nil, false, cerr
		}

		if handled {
			continue
		}

		cerr = c.checkFragment(op, started)
		if cerr != nil {
			return nil, false, cerr
		}

		if !started {
			started, isText = true, op == opText
		}

		if len(buf)+len(payload) > c.maxMessage {
			return nil, false, c.fail(CloseTooBig, "message too big")
		}

		buf = append(buf, payload...)

		if !fin {
			continue
		}

		if isText && !utf8.Valid(buf) {
			return nil, false, c.fail(CloseInvalidPayload, "text message is not valid UTF-8")
		}

		return buf, isText, nil
	}
}

// control answers ping, ignores pong and completes the closing handshake.
// handled is false for data frames.
func (c *Conn) control(op byte, payload []byte) (handled bool, err error) {
	switch op {
	case opPing:
		_ = c.writeFrame(opPong, payload)

		return true, nil
	case opPong:
		return true, nil
	case opClose:
		c.answerClose(payload)

		return true, ErrClosed
	default:
		return false, nil
	}
}

// checkFragment validates the opcode of a data frame against the fragmentation state.
func (c *Conn) checkFragment(op byte, started bool) error {
	switch op {
	case opText, opBinary:
		if started {
			return c.fail(CloseProtocolError, "new message inside a fragmented one")
		}
	case opContinuation:
		if !started {
			return c.fail(CloseProtocolError, "unexpected continuation frame")
		}
	default:
		return c.fail(CloseProtocolError, "unknown opcode")
	}

	return nil
}

// frameHeader is the decoded start of a client frame.
type frameHeader struct {
	fin    bool
	op     byte
	length uint64
}

// readHeader reads and validates the frame header (and the extended length).
func (c *Conn) readHeader() (frameHeader, error) {
	var hdr [2]byte

	_, err := io.ReadFull(c.br, hdr[:])
	if err != nil {
		return frameHeader{}, err
	}

	h := frameHeader{fin: hdr[0]&finBit != 0, op: hdr[0] & opcodeMask, length: uint64(hdr[1] & lenMask)}

	switch {
	case hdr[0]&reservedBits != 0:
		return h, c.fail(CloseProtocolError, "reserved bits are set")
	case hdr[1]&maskBit == 0:
		return h, c.fail(CloseProtocolError, "client frames must be masked")
	}

	h.length, err = c.extendedLength(h.length)
	if err != nil {
		return h, err
	}

	if h.op >= opClose && (!h.fin || h.length > maxControlPayload) {
		return h, c.fail(CloseProtocolError, "invalid control frame")
	}

	if h.length > uint64(c.maxMessage) { //nolint:gosec // maxMessage is positive.
		return h, c.fail(CloseTooBig, "frame too big")
	}

	return h, nil
}

// readFrame reads one frame from the client and unmasks its payload.
func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
	h, err := c.readHeader()
	if err != nil {
		return false, 0, nil, err
	}

	var mask [4]byte

	_, err = io.ReadFull(c.br, mask[:])
	if err != nil {
		return false, 0, nil, err
	}

	payload = make([]byte, h.length)

	_, err = io.ReadFull(c.br, payload)
	if err != nil {
		return false, 0, nil, err
	}

	for i := range payload {
		payload[i] ^= mask[i%len(mask)]
	}

	return h.fin, h.op, payload, nil
}

func (c *Conn) extendedLength(length uint64) (uint64, error) {
	switch length {
	case len16:
		var b [2]byte

		_, err := io.ReadFull(c.br, b[:])

		return uint64(binary.BigEndian.Uint16(b[:])), err
	case len64:
		var b [8]byte

		_, err := io.ReadFull(c.br, b[:])
		if err != nil {
			return 0, err
		}

		n := binary.BigEndian.Uint64(b[:])
		if n>>63 != 0 {
			return 0, c.fail(CloseProtocolError, "frame length out of range")
		}

		return n, nil
	default:
		return length, nil
	}
}

// answerClose echoes the client's close frame and drops the connection.
func (c *Conn) answerClose(payload []byte) {
	code := CloseNormal
	if len(payload) >= closeHdrLen {
		code = int(binary.BigEndian.Uint16(payload))
	}

	_ = c.Close(code, "")
}

// fail closes the connection with a protocol error and returns it.
func (c *Conn) fail(code int, reason string) error {
	_ = c.Close(code, reason)

	return fmt.Errorf("%w: %s", ErrClosed, reason)
}

func (c *Conn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	if c.closed {
		return ErrClosed
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	_, err := c.conn.Write(encodeFrame(op, payload))

	return err
}

func truncateReason(s string) string {
	const maxReason = maxControlPayload - closeHdrLen
	if len(s) > maxReason {
		s = s[:maxReason]
	}

	return s
}

// encodeFrame builds an unmasked server frame.
func encodeFrame(op byte, payload []byte) []byte {
	n := len(payload)
	frame := make([]byte, 0, n+maxHeaderLen)
	frame = append(frame, finBit|op)

	switch {
	case n < len16:
		frame = append(frame, byte(n))
	case n <= maxUint16:
		frame = append(frame, len16)
		frame = binary.BigEndian.AppendUint16(frame, uint16(n)) //nolint:gosec // n <= maxUint16 in this branch.
	default:
		var b [8]byte

		binary.BigEndian.PutUint64(b[:], uint64(n)) //nolint:gosec // n is non-negative.

		frame = append(frame, len64)
		frame = append(frame, b[:]...)
	}

	return append(frame, payload...)
}
