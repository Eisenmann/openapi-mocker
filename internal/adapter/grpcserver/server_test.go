package grpcserver_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/grpcserver"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const shopProto = `syntax = "proto3";
package shop.v1;
message Item { string id = 1; string name = 2; int64 price_cents = 3; }
message GetItemRequest { string id = 1; }
message Summary { int32 count = 1; string last_id = 2; }
service Shop {
  rpc GetItem (GetItemRequest) returns (Item);
  rpc ListItems (GetItemRequest) returns (stream Item);
  rpc Upload (stream Item) returns (Summary);
  rpc Chat (stream Item) returns (stream Item);
}`

type env struct {
	t      *testing.T
	srv    *httptest.Server
	store  *jsonstore.Store
	engine *protobuf.Engine
	pid    string
	client *http.Client
	logs   *testLogs
}

// newEnv serves the gRPC mock over h2c on a loopback listener.
func newEnv(t *testing.T) *env {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := protobuf.NewEngine()
	logs := &testLogs{}
	mocks := usecase.NewMockServingService(store, store, logs, openapi.NewEngine())
	svc := usecase.NewGRPCServingService(store, store, engine, mocks)

	p := store.Create("shop", "")
	store.AddVersion(p.ID, usecase.FormatProto, shopProto, "test")

	srv := httptest.NewUnstartedServer(grpcserver.New(svc))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)

	return &env{t: t, srv: srv, store: store, engine: engine, pid: p.ID, client: &http.Client{Transport: tr}, logs: logs}
}

func (e *env) rule(path string, r domain.MockRule) {
	e.t.Helper()

	r.ProjectID, r.Path, r.Method = e.pid, path, http.MethodPost
	if r.StatusCode == 0 {
		r.StatusCode = http.StatusOK
	}

	e.store.CreateMock(&r)
}

func frame(msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))

	return append(out, msg...)
}

func (e *env) wire(msgType, doc string) []byte {
	e.t.Helper()

	b, err := e.engine.JSONToWire([]byte(shopProto), msgType, []byte(doc))
	if err != nil {
		e.t.Fatal(err)
	}

	return b
}

func (e *env) json(msgType string, wire []byte) string {
	e.t.Helper()

	b, err := e.engine.WireToJSON([]byte(shopProto), msgType, wire)
	if err != nil {
		e.t.Fatal(err)
	}

	return string(b)
}

type result struct {
	msgs    [][]byte
	status  int
	message string
	resp    *http.Response
}

// do performs a call with the given request body and returns all messages and the status.
func (e *env) do(path string, body io.Reader, headers map[string]string) result {
	e.t.Helper()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+path, body)
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("Te", "trailers")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()

	return readResult(e.t, resp)
}

func readResult(t *testing.T, resp *http.Response) result {
	t.Helper()

	res := result{resp: resp}
	br := resp.Body

	for {
		var hdr [5]byte

		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break
		}

		msg := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(br, msg); err != nil {
			t.Fatalf("truncated message: %v", err)
		}

		res.msgs = append(res.msgs, msg)
	}

	st := resp.Trailer.Get("Grpc-Status")
	if st == "" {
		st = resp.Header.Get("Grpc-Status") // Trailers-Only.
	}

	res.status, _ = strconv.Atoi(st)
	res.message = resp.Trailer.Get("Grpc-Message")

	if res.message == "" {
		res.message = resp.Header.Get("Grpc-Message")
	}

	if st == "" {
		t.Fatalf("no grpc-status in header %v trailer %v", resp.Header, resp.Trailer)
	}

	return res
}

func TestUnary_ExampleAndRule(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	req := frame(e.wire("shop.v1.GetItemRequest", `{"id":"42"}`))

	// No rule: an example response built from the .proto.
	res := e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), nil)
	if res.status != 0 || len(res.msgs) != 1 {
		t.Fatalf("status %d, %d messages", res.status, len(res.msgs))
	}

	if got := e.json("shop.v1.Item", res.msgs[0]); !strings.Contains(got, `"name":"name"`) {
		t.Errorf("example: %s", got)
	}

	if ct := res.resp.Header.Get("Content-Type"); ct != "application/grpc" {
		t.Errorf("content type %q", ct)
	}

	// A mock rule with templates over the decoded request.
	e.rule("/shop.v1.Shop/GetItem", domain.MockRule{Body: `{"id":"{{request.body.id}}","name":"Lamp","priceCents":"1999"}`})

	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), nil)
	if got := e.json("shop.v1.Item", res.msgs[0]); !strings.Contains(got, `"id":"42"`) || !strings.Contains(got, `"priceCents":"1999"`) {
		t.Errorf("rule response: %s", got)
	}
}

func TestUnary_ErrorStatuses(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	req := frame(e.wire("shop.v1.GetItemRequest", `{"id":"x"}`))

	e.rule("/shop.v1.Shop/GetItem", domain.MockRule{StatusCode: http.StatusNotFound, Body: `{"message":"no such item: ü"}`, Scenario: "missing"})

	res := e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), map[string]string{"X-Mock-Scenario": "missing"})
	if res.status != usecase.GRPCNotFound || len(res.msgs) != 0 || res.message != "no such item: %C3%BC" {
		t.Errorf("status %d msg %q msgs %d", res.status, res.message, len(res.msgs))
	}

	// Unknown method.
	res = e.do("/shop.v1.Shop/Nope", bytes.NewReader(req), nil)
	if res.status != usecase.GRPCUnimplemented {
		t.Errorf("unknown method: %d", res.status)
	}

	// Garbage that is not a valid message.
	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(frame([]byte{0xff, 0xff})), nil)
	if res.status != usecase.GRPCInvalidArgument {
		t.Errorf("garbage: %d %s", res.status, res.message)
	}

	// A mock body that does not fit the response type is the mock's fault: INTERNAL.
	e.rule("/shop.v1.Shop/GetItem", domain.MockRule{Body: `{"bogus":1}`})

	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), nil)
	if res.status != usecase.GRPCInternal || !strings.Contains(res.message, "bogus") {
		t.Errorf("bad mock body: %d %q", res.status, res.message)
	}

	// No request message at all.
	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(nil), nil)
	if res.status != usecase.GRPCInternal {
		t.Errorf("empty call: %d", res.status)
	}
}

func TestPlainHTTPIsRejected(t *testing.T) {
	t.Parallel()

	e := newEnv(t)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/shop.v1.Shop/GetItem", nil)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestServerStreaming(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	req := frame(e.wire("shop.v1.GetItemRequest", `{"id":"1"}`))

	// Default: three example messages.
	res := e.do("/shop.v1.Shop/ListItems", bytes.NewReader(req), nil)
	if res.status != 0 || len(res.msgs) != 3 {
		t.Fatalf("default stream: status %d, %d messages", res.status, len(res.msgs))
	}

	// A JSON array body is one message per element.
	e.rule("/shop.v1.Shop/ListItems", domain.MockRule{Scenario: "array", Body: `[{"id":"a"},{"id":"b"}]`})

	res = e.do("/shop.v1.Shop/ListItems", bytes.NewReader(req), map[string]string{"X-Mock-Scenario": "array"})
	if len(res.msgs) != 2 || !strings.Contains(e.json("shop.v1.Item", res.msgs[1]), `"id":"b"`) {
		t.Errorf("array stream: %d messages", len(res.msgs))
	}

	// A stream script is played with its timing; templates render per message.
	e.rule("/shop.v1.Shop/ListItems", domain.MockRule{
		Scenario: "script",
		Stream: &domain.StreamScript{Steps: []domain.StreamStep{
			{Data: `{"id":"n{{counter}}"}`, Repeat: 3, IntervalMs: 5},
			{Data: `{"id":"last"}`, DelayMs: 5},
		}},
	})

	res = e.do("/shop.v1.Shop/ListItems", bytes.NewReader(req), map[string]string{"X-Mock-Scenario": "script"})
	if res.status != 0 || len(res.msgs) != 4 {
		t.Fatalf("script stream: status %d, %d messages", res.status, len(res.msgs))
	}

	if a, b := e.json("shop.v1.Item", res.msgs[0]), e.json("shop.v1.Item", res.msgs[1]); a == b {
		t.Errorf("messages should differ per emission: %s %s", a, b)
	}

	// A stream script on a unary method is a configuration error.
	e.rule("/shop.v1.Shop/GetItem", domain.MockRule{Stream: &domain.StreamScript{Steps: []domain.StreamStep{{Data: `{}`}}}})

	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), nil)
	if res.status != usecase.GRPCInternal {
		t.Errorf("stream script on unary: %d", res.status)
	}
}

func TestClientStreaming(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.rule("/shop.v1.Shop/Upload", domain.MockRule{Body: `{"count":3,"lastId":"{{request.body.id}}"}`})

	var body bytes.Buffer
	for _, id := range []string{"a", "b", "c"} {
		body.Write(frame(e.wire("shop.v1.Item", `{"id":"`+id+`"}`)))
	}

	res := e.do("/shop.v1.Shop/Upload", &body, nil)
	if res.status != 0 || len(res.msgs) != 1 {
		t.Fatalf("status %d, %d messages", res.status, len(res.msgs))
	}

	if got := e.json("shop.v1.Summary", res.msgs[0]); !strings.Contains(got, `"count":3`) || !strings.Contains(got, `"lastId":"c"`) {
		t.Errorf("summary: %s", got)
	}
}

func TestBidirectionalStreaming(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.rule("/shop.v1.Shop/Chat", domain.MockRule{Body: `{"id":"echo-{{request.body.id}}"}`})

	pr, pw := io.Pipe()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/shop.v1.Shop/Chat", pr)
	req.Header.Set("Content-Type", "application/grpc")

	type out struct {
		resp *http.Response
		err  error
	}

	done := make(chan out, 1)

	go func() {
		resp, err := e.client.Do(req)
		done <- out{resp, err}
	}()

	// Send the first message and read its answer before sending the second:
	// the server must reply while the request is still open.
	_, _ = pw.Write(frame(e.wire("shop.v1.Item", `{"id":"1"}`)))

	o := <-done
	if o.err != nil {
		t.Fatal(o.err)
	}
	defer o.resp.Body.Close()

	readOne := func() string {
		var hdr [5]byte
		if _, err := io.ReadFull(o.resp.Body, hdr[:]); err != nil {
			t.Fatalf("read: %v", err)
		}

		msg := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		_, _ = io.ReadFull(o.resp.Body, msg)

		return e.json("shop.v1.Item", msg)
	}

	if got := readOne(); !strings.Contains(got, `"id":"echo-1"`) {
		t.Errorf("first reply: %s", got)
	}

	_, _ = pw.Write(frame(e.wire("shop.v1.Item", `{"id":"2"}`)))

	if got := readOne(); !strings.Contains(got, `"id":"echo-2"`) {
		t.Errorf("second reply: %s", got)
	}

	pw.Close()

	rest, _ := io.ReadAll(o.resp.Body)
	if len(rest) != 0 || o.resp.Trailer.Get("Grpc-Status") != "0" {
		t.Errorf("end of call: %d extra bytes, status %q", len(rest), o.resp.Trailer.Get("Grpc-Status"))
	}
}

func TestCompressedRequestAndDeadline(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	plain := e.wire("shop.v1.GetItemRequest", `{"id":"gz"}`)

	var zbuf bytes.Buffer

	zw := gzip.NewWriter(&zbuf)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	compressed := append([]byte{1}, frame(zbuf.Bytes())[1:]...)

	e.rule("/shop.v1.Shop/GetItem", domain.MockRule{Body: `{"id":"{{request.body.id}}"}`})

	res := e.do("/shop.v1.Shop/GetItem", bytes.NewReader(compressed), map[string]string{"Grpc-Encoding": "gzip"})
	if res.status != 0 || !strings.Contains(e.json("shop.v1.Item", res.msgs[0]), `"id":"gz"`) {
		t.Errorf("compressed request: status %d", res.status)
	}

	// An unknown encoding is rejected.
	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(compressed), map[string]string{"Grpc-Encoding": "br"})
	if res.status != usecase.GRPCInvalidArgument {
		t.Errorf("unsupported encoding: %d", res.status)
	}

	// A latency longer than the client's deadline ends with DEADLINE_EXCEEDED.
	e.rule("/shop.v1.Shop/GetItem", domain.MockRule{
		Scenario: "slow", Body: `{}`, DelayMs: 2000,
	})

	start := time.Now()
	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(frame(plain)), map[string]string{"X-Mock-Scenario": "slow", "Grpc-Timeout": "100m"})

	if res.status != usecase.GRPCDeadlineExceeded || time.Since(start) > time.Second {
		t.Errorf("deadline: status %d after %v", res.status, time.Since(start))
	}
}

func TestProjectSelection(t *testing.T) {
	t.Parallel()

	e := newEnv(t)

	// A second project defining the same service makes the call ambiguous.
	p2 := e.store.Create("shop2", "")
	e.store.AddVersion(p2.ID, usecase.FormatProto, shopProto, "test")

	req := frame(e.wire("shop.v1.GetItemRequest", `{"id":"1"}`))

	res := e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), nil)
	if res.status != usecase.GRPCFailedPrecondition || !strings.Contains(res.message, "x-mock-project") {
		t.Errorf("ambiguous call: %d %q", res.status, res.message)
	}

	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), map[string]string{"X-Mock-Project": p2.ID})
	if res.status != 0 {
		t.Errorf("selected project: %d %q", res.status, res.message)
	}

	res = e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), map[string]string{"X-Mock-Project": "missing"})
	if res.status != usecase.GRPCNotFound {
		t.Errorf("unknown project: %d", res.status)
	}
}

func TestReflection(t *testing.T) {
	t.Parallel()

	e := newEnv(t)

	// list_services = field 7 (string).
	list := append([]byte{0x3a, 0x01}, '*')
	res := e.do("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", bytes.NewReader(frame(list)), nil)

	if res.status != 0 || len(res.msgs) != 1 || !bytes.Contains(res.msgs[0], []byte("shop.v1.Shop")) {
		t.Fatalf("list_services: status %d msgs %d", res.status, len(res.msgs))
	}

	// file_containing_symbol = field 4 (string); v1alpha path answers the same.
	sym := append([]byte{0x22, byte(len("shop.v1.Shop"))}, "shop.v1.Shop"...)
	res = e.do("/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo", bytes.NewReader(frame(sym)), nil)

	if res.status != 0 || len(res.msgs) != 1 || !bytes.Contains(res.msgs[0], []byte("GetItem")) {
		t.Fatalf("file_containing_symbol: status %d msgs %d", res.status, len(res.msgs))
	}

	// A symbol that does not exist is an error_response (field 7), not a failed call.
	bad := append([]byte{0x22, 0x04}, "nope"...)
	res = e.do("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", bytes.NewReader(frame(bad)), nil)

	if res.status != 0 || len(res.msgs) != 1 || !bytes.Contains(res.msgs[0], []byte("not found")) {
		t.Errorf("unknown symbol: status %d", res.status)
	}

	// file_by_filename = field 3 for a well-known type.
	name := "google/protobuf/timestamp.proto"
	byName := append([]byte{0x1a, byte(len(name))}, name...)
	res = e.do("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", bytes.NewReader(frame(byName)), nil)

	if res.status != 0 || !bytes.Contains(res.msgs[0], []byte("Timestamp")) {
		t.Errorf("file_by_filename: status %d", res.status)
	}
}

func TestRequestsAreLogged(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	req := frame(e.wire("shop.v1.GetItemRequest", `{"id":"7"}`))
	e.do("/shop.v1.Shop/GetItem", bytes.NewReader(req), nil)

	var found *domain.RequestLog

	for _, l := range e.logs.ListLogs("", 0) {
		if l.Path == "/shop.v1.Shop/GetItem" {
			found = l
		}
	}

	if found == nil || found.Endpoint != "/shop.v1.Shop/GetItem" || found.StatusCode != http.StatusOK {
		t.Fatalf("log entry: %+v", found)
	}

	var body map[string]any
	if json.Unmarshal([]byte(found.Request.Body), &body) != nil || body["id"] != "7" {
		t.Errorf("request body in the log: %q", found.Request.Body)
	}
}
