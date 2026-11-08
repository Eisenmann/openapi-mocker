// Package grpcserver serves the gRPC mock: gRPC over HTTP/2 without TLS (h2c),
// built on net/http only. It speaks the gRPC wire protocol (length-prefixed
// messages, trailers, deadlines, gzip request compression) and delegates every
// decision to usecase.GRPCServingService.
package grpcserver

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	contentTypePrefix = "application/grpc"
	// maxMessageBytes is the largest message accepted from a client (gRPC's default).
	maxMessageBytes = 4 << 20
	// readHeaderTimeout bounds how long a client may take to send its headers.
	readHeaderTimeout = 10 * time.Second
	// maxClientStreamMessages bounds a client stream.
	maxClientStreamMessages = 100000
	frameHeaderLen          = 5

	reflectionV1      = "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo"
	reflectionV1Alpha = "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo"
)

// Server is an http.Handler for gRPC calls.
type Server struct {
	svc *usecase.GRPCServingService
}

// New creates the handler.
func New(svc *usecase.GRPCServingService) *Server { return &Server{svc: svc} }

// HTTPServer returns an http.Server that accepts HTTP/2 over cleartext TCP
// (what gRPC clients use without TLS) at addr.
func (s *Server) HTTPServer(addr string) *http.Server {
	var protocols http.Protocols

	protocols.SetUnencryptedHTTP2(true)

	return &http.Server{ //nolint:exhaustruct,exhaustruct_v5 // streaming calls need no read/write/idle timeouts.
		Addr:              addr,
		Handler:           s,
		Protocols:         &protocols,
		ReadHeaderTimeout: readHeaderTimeout,
	}
}

// EnableTLS makes srv (from HTTPServer) serve gRPC over TLS with the given
// certificate and key (PEM files). Start it with ListenAndServeTLS("", "");
// IsTLS reports whether that is needed.
func EnableTLS(srv *http.Server, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("loading the gRPC TLS certificate: %w", err)
	}

	srv.TLSConfig = &tls.Config{ //nolint:exhaustruct,exhaustruct_v5 // defaults are fine.
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}

	var protocols http.Protocols

	protocols.SetHTTP2(true)

	srv.Protocols = &protocols

	return nil
}

// IsTLS reports whether srv was configured with EnableTLS.
func IsTLS(srv *http.Server) bool {
	return srv != nil && srv.TLSConfig != nil && len(srv.TLSConfig.Certificates) > 0
}

var (
	errNoMessage         = errors.New("the call carried no request message")
	errMessageTooLarge   = errors.New("message exceeds the size limit")
	errUnsupportedCoding = errors.New("unsupported message encoding")
	errDecompressed      = errors.New("decompressed message is too large")
)

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), contentTypePrefix) {
		http.Error(w, "this port speaks gRPC (HTTP/2, content-type application/grpc)", http.StatusUnsupportedMediaType)

		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	//nolint:exhaustruct,exhaustruct_v5 // ctx, hint and target are set right below.
	c := &call{w: w, r: r, rc: http.NewResponseController(w), svc: s.svc}

	ctx, cancel := withTimeout(r.Context(), r.Header.Get("Grpc-Timeout"))
	defer cancel()

	c.ctx = ctx
	c.hint = r.Header.Get(usecase.GRPCProjectMetadata)

	switch r.URL.Path {
	case reflectionV1, reflectionV1Alpha:
		c.reflect()

		return
	}

	target, gerr := s.svc.Resolve(c.hint, r.URL.Path)
	if gerr != nil {
		c.finish(gerr.Code, gerr.Message)

		return
	}

	c.target = target

	switch {
	case target.Method.ClientStreaming && target.Method.ServerStreaming:
		c.bidi()
	case target.Method.ServerStreaming:
		c.serverStream()
	default:
		c.unary()
	}
}

// withTimeout applies the grpc-timeout header ("<digits><unit>").
func withTimeout(ctx context.Context, header string) (context.Context, context.CancelFunc) {
	if len(header) < 2 { //nolint:mnd // at least one digit and a unit.
		return context.WithCancel(ctx)
	}

	n, err := strconv.ParseInt(header[:len(header)-1], 10, 64)
	if err != nil || n < 0 {
		return context.WithCancel(ctx)
	}

	units := map[byte]time.Duration{
		'H': time.Hour, 'M': time.Minute, 'S': time.Second,
		'm': time.Millisecond, 'u': time.Microsecond, 'n': time.Nanosecond,
	}

	unit, ok := units[header[len(header)-1]]
	if !ok {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, time.Duration(n)*unit)
}

// call is the state of one RPC.
type call struct {
	w      http.ResponseWriter
	r      *http.Request
	rc     *http.ResponseController
	svc    *usecase.GRPCServingService
	ctx    context.Context //nolint:containedctx // a call is a short-lived request scope.
	hint   string
	target *usecase.GRPCTarget
	wrote  bool
}

// metadata is the request metadata that steers the mock rules.
func (c *call) metadata() map[string][]string {
	md := map[string][]string{}

	for k, v := range c.r.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || lk == "te" || strings.HasPrefix(lk, "grpc-") {
			continue
		}

		md[lk] = v
	}

	return md
}

func (c *call) startResponse() {
	if c.wrote {
		return
	}

	c.wrote = true
	c.w.Header().Set("Content-Type", contentTypePrefix)
	c.w.Header().Set("Grpc-Accept-Encoding", "gzip")
	c.w.WriteHeader(http.StatusOK)
}

// send writes one message and flushes it.
func (c *call) send(msg []byte) error {
	c.startResponse()

	frame := make([]byte, frameHeaderLen, frameHeaderLen+len(msg))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(msg))) //nolint:gosec // bounded by the message encoder.
	frame = append(frame, msg...)

	_, err := c.w.Write(frame)
	if err != nil {
		return err
	}

	return c.rc.Flush()
}

// finish ends the call with a status. Before any message it is a
// "Trailers-Only" response, afterwards the status travels in the trailers.
func (c *call) finish(code int, message string) {
	h := c.w.Header()

	if !c.wrote {
		h.Set("Content-Type", contentTypePrefix)
		h.Set("Grpc-Status", strconv.Itoa(code))

		if message != "" {
			h.Set("Grpc-Message", encodeMessage(message))
		}

		c.wrote = true
		c.w.WriteHeader(http.StatusOK)

		return
	}

	h.Set(http.TrailerPrefix+"Grpc-Status", strconv.Itoa(code))

	if message != "" {
		h.Set(http.TrailerPrefix+"Grpc-Message", encodeMessage(message))
	}
}

// encodeMessage percent-encodes a status message as the gRPC spec requires.
func encodeMessage(s string) string {
	var b strings.Builder

	for i := range len(s) {
		ch := s[i]
		if ch >= 0x20 && ch <= 0x7e && ch != '%' { //nolint:mnd // printable ASCII.
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}

	return b.String()
}

// readMessage reads the next request message; io.EOF means the client is done.
func (c *call) readMessage(br *bufio.Reader) ([]byte, error) {
	var hdr [frameHeaderLen]byte

	_, err := io.ReadFull(br, hdr[:])
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("truncated message header: %w", err)
		}

		return nil, err
	}

	size := binary.BigEndian.Uint32(hdr[1:])
	if size > maxMessageBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", errMessageTooLarge, size, maxMessageBytes)
	}

	msg := make([]byte, size)

	_, err = io.ReadFull(br, msg)
	if err != nil {
		return nil, fmt.Errorf("truncated message: %w", err)
	}

	if hdr[0] == 0 {
		return msg, nil
	}

	if enc := c.r.Header.Get("Grpc-Encoding"); enc != "gzip" {
		return nil, fmt.Errorf("%w %q", errUnsupportedCoding, enc)
	}

	zr, err := gzip.NewReader(bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	out, err := io.ReadAll(io.LimitReader(zr, maxMessageBytes+1))
	if err != nil {
		return nil, err
	}

	if len(out) > maxMessageBytes {
		return nil, errDecompressed
	}

	return out, nil
}

// sleep waits for the mock latency; false means the call was cancelled.
func (c *call) sleep(ms int) bool {
	if ms <= 0 {
		return true
	}

	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()

	select {
	case <-t.C:
		return true
	case <-c.ctx.Done():
		return false
	}
}

// cancelled ends the call because the client left or the deadline passed.
func (c *call) cancelled() {
	if errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
		c.finish(usecase.GRPCDeadlineExceeded, "deadline exceeded")

		return
	}

	c.finish(usecase.GRPCCancelled, "call cancelled")
}

// readRequest reads the request message(s) of a unary or client-streaming
// call and returns the last one. ok is false when the call was finished.
func (c *call) readRequest() (last []byte, ok bool) {
	br := bufio.NewReader(c.r.Body)
	count := 0

	for {
		msg, err := c.readMessage(br)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			c.finish(usecase.GRPCInvalidArgument, err.Error())

			return nil, false
		}

		last = msg
		count++

		if !c.target.Method.ClientStreaming {
			break // a unary call carries exactly one message.
		}

		if count >= maxClientStreamMessages {
			c.finish(usecase.GRPCResourceExhausted, "too many messages in the client stream")

			return nil, false
		}
	}

	if count == 0 && !c.target.Method.ClientStreaming {
		c.finish(usecase.GRPCInternal, errNoMessage.Error())

		return nil, false
	}

	return last, true
}

func (c *call) unary() {
	last, ok := c.readRequest()
	if !ok {
		return
	}

	c.emit(c.svc.Reply(c.ctx, c.target, c.metadata(), last))
}

func (c *call) serverStream() {
	msg, err := c.readMessage(bufio.NewReader(c.r.Body))
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = errNoMessage
		}

		c.finish(usecase.GRPCInvalidArgument, err.Error())

		return
	}

	c.emit(c.svc.Reply(c.ctx, c.target, c.metadata(), msg))
}

// emit sends a reply (its messages or its stream) and finishes the call
// with the reply's status.
func (c *call) emit(reply *usecase.GRPCReply) {
	if !c.sleep(reply.DelayMs) {
		c.cancelled()

		return
	}

	if reply.Code != usecase.GRPCOK {
		c.finish(reply.Code, reply.Message)

		return
	}

	if c.playReply(reply) {
		c.finish(usecase.GRPCOK, "")
	}
}

// playReply sends the messages of a reply; it reports false when the call has
// already been finished (or the client is gone).
func (c *call) playReply(reply *usecase.GRPCReply) bool {
	for _, m := range reply.Messages {
		if c.send(m) != nil {
			return false
		}
	}

	if reply.Stream == nil {
		return true
	}

	var encodeErr error

	err := reply.Stream.Play(c.ctx, func(ev usecase.StreamEvent) error {
		wire, werr := reply.Encode([]byte(ev.Data))
		if werr != nil {
			encodeErr = werr

			return werr
		}

		return c.send(wire)
	})

	switch {
	case encodeErr != nil:
		c.finish(usecase.GRPCInternal, "stream message does not match the response type: "+encodeErr.Error())

		return false
	case err != nil:
		return false
	case c.ctx.Err() != nil:
		c.cancelled()

		return false
	}

	return true
}

// readFailed ends the call after a request read error.
func (c *call) readFailed(err error) {
	if c.ctx.Err() != nil {
		c.cancelled()

		return
	}

	c.finish(usecase.GRPCInvalidArgument, err.Error())
}

func (c *call) bidi() {
	br := bufio.NewReader(c.r.Body)
	streamed := false

	for {
		msg, err := c.readMessage(br)
		if errors.Is(err, io.EOF) {
			c.finish(usecase.GRPCOK, "")

			return
		}

		if err != nil {
			c.readFailed(err)

			return
		}

		reply := c.svc.Reply(c.ctx, c.target, c.metadata(), msg)

		// A stream script plays once, for the first request.
		if reply.Stream != nil && streamed {
			reply.Stream = nil
		}

		streamed = streamed || reply.Stream != nil

		if !c.sleep(reply.DelayMs) {
			c.cancelled()

			return
		}

		if reply.Code != usecase.GRPCOK {
			c.finish(reply.Code, reply.Message)

			return
		}

		if !c.playReply(reply) {
			return
		}
	}
}
