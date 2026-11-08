package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// gRPC status codes (google.golang.org/grpc/codes).
const (
	GRPCOK                 = 0
	GRPCCancelled          = 1
	GRPCUnknown            = 2
	GRPCInvalidArgument    = 3
	GRPCDeadlineExceeded   = 4
	GRPCNotFound           = 5
	GRPCAlreadyExists      = 6
	GRPCPermissionDenied   = 7
	GRPCResourceExhausted  = 8
	GRPCFailedPrecondition = 9
	GRPCAborted            = 10
	GRPCOutOfRange         = 11
	GRPCUnimplemented      = 12
	GRPCInternal           = 13
	GRPCUnavailable        = 14
	GRPCDataLoss           = 15
	GRPCUnauthenticated    = 16
)

// gRPC metadata keys that steer the mock.
const (
	// GRPCProjectMetadata selects the project when several define a service.
	GRPCProjectMetadata = "x-mock-project"
	// GRPCScenarioMetadata selects a named mock scenario.
	GRPCScenarioMetadata = "x-mock-scenario"

	// defaultStreamMessages is how many example messages a server stream gets
	// when no mock rule is defined.
	defaultStreamMessages = 3
)

// GRPCError is a failed call: a status code and a message.
type GRPCError struct {
	Code    int
	Message string
}

func (e *GRPCError) Error() string { return fmt.Sprintf("grpc %d: %s", e.Code, e.Message) }

func grpcErr(code int, format string, args ...any) *GRPCError {
	return &GRPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// GRPCTarget is a resolved method of a project.
type GRPCTarget struct {
	ProjectID string
	Service   string
	Method    GRPCMethod
	raw       string
}

// GRPCReply is what to send for one call (or, for bidi, one request message).
type GRPCReply struct {
	// Code is the status the call ends with (GRPCOK on success).
	Code    int
	Message string
	// DelayMs is the latency to add before replying.
	DelayMs int
	// Messages are the protobuf-encoded response messages.
	Messages [][]byte
	// Stream, when set, replaces Messages: every event is one message
	// (Encode turns its JSON into protobuf).
	Stream *StreamPlan
	Encode func(doc []byte) ([]byte, error)
}

// GRPCServingService answers gRPC calls from the mock rules and the .proto
// contract of a project. Like the other serving services it decides WHAT to
// answer; the gRPC adapter does the HTTP/2 IO.
type GRPCServingService struct {
	projects  ProjectRepository
	contracts ContractRepository
	engine    ProtoEngine
	mocks     *MockServingService
}

// NewGRPCServingService assembles the service.
func NewGRPCServingService(
	projects ProjectRepository, contracts ContractRepository, engine ProtoEngine, mocks *MockServingService,
) *GRPCServingService {
	return &GRPCServingService{projects: projects, contracts: contracts, engine: engine, mocks: mocks}
}

// Services lists the services (with sample messages) of a project's .proto contract.
func (s *GRPCServingService) Services(projectID string) ([]GRPCService, error) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return nil, err
	}

	if c.Format != FormatProto {
		return nil, ErrNotProtoContract
	}

	svcs, err := s.engine.Services([]byte(c.Raw))
	if err != nil {
		return nil, err
	}

	for i := range svcs {
		for j := range svcs[i].Methods {
			m := &svcs[i].Methods[j]

			m.RequestExample = s.exampleOrEmpty(c.Raw, m.InputType)
			m.ResponseExample = s.exampleOrEmpty(c.Raw, m.OutputType)
		}
	}

	return svcs, nil
}

// GRPCMethodLookup tells whether a gRPC method streams its responses.
type GRPCMethodLookup interface {
	// MethodShape reports whether the method at path ("/pkg.Service/Method")
	// of the project's .proto contract streams responses; known is false when
	// the project has no such method (or no .proto contract).
	MethodShape(projectID, path string) (serverStreaming, known bool)
}

var _ GRPCMethodLookup = (*GRPCServingService)(nil)

// MethodShape implements GRPCMethodLookup.
func (s *GRPCServingService) MethodShape(projectID, path string) (serverStreaming, known bool) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil || c.Format != FormatProto {
		return false, false
	}

	svcs, err := s.engine.Services([]byte(c.Raw))
	if err != nil {
		return false, false
	}

	for i := range svcs {
		for j := range svcs[i].Methods {
			if m := &svcs[i].Methods[j]; m.Path == path {
				return m.ServerStreaming, true
			}
		}
	}

	return false, false
}

// exampleOrEmpty is the sample JSON of a message type ("" if it cannot be built).
func (s *GRPCServingService) exampleOrEmpty(raw, message string) string {
	ex, err := s.engine.Example([]byte(raw), message)
	if err != nil {
		return ""
	}

	return string(ex)
}

// ErrNotProtoContract is returned when a project's active contract is not a .proto file.
var ErrNotProtoContract = errors.New("project is not a gRPC (.proto) contract")

// protoSource returns the .proto source of a project ("" if it has none).
func (s *GRPCServingService) protoSource(projectID string) string {
	c, err := s.contracts.GetActive(projectID)
	if err != nil || c.Format != FormatProto {
		return ""
	}

	return c.Raw
}

// protoContracts maps project ids to .proto sources: for all projects, or
// only the hinted one.
func (s *GRPCServingService) protoContracts(hint string) map[string]string {
	out := map[string]string{}

	if hint != "" {
		if raw := s.protoSource(hint); raw != "" {
			out[hint] = raw
		}

		return out
	}

	for _, p := range s.projects.List() {
		if raw := s.protoSource(p.ID); raw != "" {
			out[p.ID] = raw
		}
	}

	return out
}

// Resolve finds the method a call path ("/pkg.Service/Method") refers to.
// hint is the project id from the x-mock-project metadata ("" to search).
func (s *GRPCServingService) Resolve(hint, path string) (*GRPCTarget, *GRPCError) {
	service, method, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !ok || service == "" || method == "" {
		return nil, grpcErr(GRPCUnimplemented, "malformed method name %q", path)
	}

	candidates := s.protoContracts(hint)
	if hint != "" && len(candidates) == 0 {
		return nil, grpcErr(GRPCNotFound, "project %q has no .proto contract", hint)
	}

	found := s.findMethod(candidates, service, method)

	switch len(found) {
	case 0:
		return nil, grpcErr(GRPCUnimplemented, "unknown method %s: no project defines it", path)
	case 1:
		return found[0], nil
	default:
		return nil, grpcErr(GRPCFailedPrecondition,
			"%s is defined by %d projects; set the %s metadata to the project id", path, len(found), GRPCProjectMetadata)
	}
}

// findMethod lists the projects (id -> source) that define service/method.
func (s *GRPCServingService) findMethod(contracts map[string]string, service, method string) []*GRPCTarget {
	var found []*GRPCTarget

	for pid, raw := range contracts {
		svcs, err := s.engine.Services([]byte(raw))
		if err != nil {
			continue
		}

		for _, svc := range svcs {
			if svc.Name != service {
				continue
			}

			for _, m := range svc.Methods {
				if m.Name == method {
					found = append(found, &GRPCTarget{ProjectID: pid, Service: svc.Name, Method: m, raw: raw})
				}
			}
		}
	}

	return found
}

// Reply answers one request message (protobuf-encoded) for a resolved method.
func (s *GRPCServingService) Reply(
	ctx context.Context, t *GRPCTarget, md map[string][]string, reqWire []byte,
) *GRPCReply {
	reqJSON, err := s.engine.WireToJSON([]byte(t.raw), t.Method.InputType, reqWire)
	if err != nil {
		return failed(GRPCInvalidArgument, "cannot decode the request as %s: %v", t.Method.InputType, err)
	}

	header := http.Header{}

	for k, v := range md {
		for _, one := range v {
			header.Add(k, one)
		}
	}

	resp := s.mocks.ServeRPC(t.ProjectID, t.Method.Path, &MockRequest{
		Method: http.MethodPost, Path: t.Method.Path, Scenario: header.Get(GRPCScenarioMetadata),
		Header: header, Body: reqJSON, Context: ctx, SkipValidation: true,
	}, func() ([]byte, error) { return s.defaultBody(t) })

	reply := &GRPCReply{Code: GRPCOK, DelayMs: resp.DelayMs}

	if code := grpcCodeOf(&resp); code != GRPCOK {
		reply.Code, reply.Message = code, grpcMessageOf(&resp)

		return reply
	}

	encode := func(doc []byte) ([]byte, error) {
		return s.engine.JSONToWire([]byte(t.raw), t.Method.OutputType, doc)
	}

	if resp.Stream != nil {
		if !t.Method.ServerStreaming {
			return failed(GRPCInternal, "%s is not server-streaming, so its mock cannot be a stream script", t.Method.Path)
		}

		reply.Stream, reply.Encode = resp.Stream, encode

		return reply
	}

	docs, err := splitMessages(resp.Body, t.Method.ServerStreaming)
	if err != nil {
		return failed(GRPCInternal, "mock response for %s: %v", t.Method.Path, err)
	}

	for _, d := range docs {
		wire, werr := encode(d)
		if werr != nil {
			return failed(GRPCInternal, "mock response for %s does not match %s: %v", t.Method.Path, t.Method.OutputType, werr)
		}

		reply.Messages = append(reply.Messages, wire)
	}

	return reply
}

func failed(code int, format string, args ...any) *GRPCReply {
	return &GRPCReply{Code: code, Message: fmt.Sprintf(format, args...)}
}

// defaultBody is the response when no rule applies: an example message, or a
// few of them for a server stream.
func (s *GRPCServingService) defaultBody(t *GRPCTarget) ([]byte, error) {
	ex, err := s.engine.Example([]byte(t.raw), t.Method.OutputType)
	if err != nil {
		return nil, err
	}

	if !t.Method.ServerStreaming {
		return ex, nil
	}

	parts := make([][]byte, defaultStreamMessages)
	for i := range parts {
		parts[i] = ex
	}

	return append(append([]byte("["), bytes.Join(parts, []byte(","))...), ']'), nil
}

// splitMessages turns a mock body into the messages to send: a JSON array is
// several messages for streaming methods; otherwise it is one message.
func splitMessages(body []byte, streaming bool) ([][]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return [][]byte{[]byte("{}")}, nil
	}

	if trimmed[0] != '[' {
		return [][]byte{trimmed}, nil
	}

	if !streaming {
		return nil, errJSONArrayNotStreaming
	}

	var items []json.RawMessage

	err := json.Unmarshal(trimmed, &items)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON array: %w", err)
	}

	out := make([][]byte, len(items))
	for i, it := range items {
		out[i] = it
	}

	return out, nil
}

// grpcCodeByHTTP maps HTTP statuses of a mock response to gRPC codes.
var grpcCodeByHTTP = map[int]int{ //nolint:gochecknoglobals // read-only lookup table.
	http.StatusBadRequest:            GRPCInvalidArgument,
	http.StatusUnauthorized:          GRPCUnauthenticated,
	http.StatusForbidden:             GRPCPermissionDenied,
	http.StatusNotFound:              GRPCNotFound,
	http.StatusConflict:              GRPCAlreadyExists,
	http.StatusPreconditionFailed:    GRPCFailedPrecondition,
	http.StatusTooManyRequests:       GRPCResourceExhausted,
	httpClientClosed:                 GRPCCancelled,
	http.StatusNotImplemented:        GRPCUnimplemented,
	http.StatusServiceUnavailable:    GRPCUnavailable,
	http.StatusGatewayTimeout:        GRPCDeadlineExceeded,
	http.StatusRequestTimeout:        GRPCDeadlineExceeded,
	http.StatusUnprocessableEntity:   GRPCInvalidArgument,
	http.StatusRequestEntityTooLarge: GRPCOutOfRange,
}

// grpcCodeOf maps the HTTP status of a mock response to a gRPC code.
func grpcCodeOf(r *MockResponse) int {
	if r.Fault == FaultDrop || r.Fault == FaultTimeout {
		return GRPCUnavailable
	}

	c := r.StatusCode

	if c >= StatusOK && c < http300 {
		return GRPCOK
	}

	if code, ok := grpcCodeByHTTP[c]; ok {
		return code
	}

	if c >= http.StatusInternalServerError {
		return GRPCInternal
	}

	return GRPCInvalidArgument
}

const (
	http300          = 300
	httpClientClosed = 499
)

// grpcMessageOf extracts a status message from an error body.
func grpcMessageOf(r *MockResponse) string {
	var obj map[string]any

	if json.Unmarshal(r.Body, &obj) == nil {
		for _, k := range []string{"message", "error", "detail"} {
			if s, ok := obj[k].(string); ok && s != "" {
				return s
			}
		}
	}

	if msg := strings.TrimSpace(string(r.Body)); msg != "" && len(msg) <= 200 {
		return msg
	}

	return http.StatusText(r.StatusCode)
}

// Server reflection.

// sortedContracts returns the .proto contracts in a stable order.
func (s *GRPCServingService) sortedContracts(hint string) (ids []string, raws map[string]string) {
	raws = s.protoContracts(hint)

	for id := range raws {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	return ids, raws
}

// ReflectServices lists the service names of the (hinted) project(s).
func (s *GRPCServingService) ReflectServices(hint string) []string {
	ids, raws := s.sortedContracts(hint)
	seen := map[string]bool{}

	var out []string

	for _, id := range ids {
		svcs, err := s.engine.Services([]byte(raws[id]))
		if err != nil {
			continue
		}

		for _, svc := range svcs {
			if !seen[svc.Name] {
				seen[svc.Name] = true
				out = append(out, svc.Name)
			}
		}
	}

	sort.Strings(out)

	return out
}

// ReflectSymbol returns the serialized file descriptors that declare a
// symbol: the file first, then the files it imports.
func (s *GRPCServingService) ReflectSymbol(hint, symbol string) ([][]byte, *GRPCError) {
	ids, raws := s.sortedContracts(hint)

	for _, id := range ids {
		raw := []byte(raws[id])

		name, _, err := s.engine.FileContaining(raw, symbol)
		if err != nil {
			continue
		}

		return s.fileWithDeps(raw, name)
	}

	return nil, grpcErr(GRPCNotFound, "symbol %q not found", symbol)
}

// ReflectFile returns the serialized descriptors of a file and its imports.
func (s *GRPCServingService) ReflectFile(hint, name string) ([][]byte, *GRPCError) {
	ids, raws := s.sortedContracts(hint)

	for _, id := range ids {
		raw := []byte(raws[id])

		_, err := s.engine.FileDescriptor(raw, name)
		if err == nil {
			return s.fileWithDeps(raw, name)
		}
	}

	return nil, grpcErr(GRPCNotFound, "file %q not found", name)
}

func (s *GRPCServingService) fileWithDeps(raw []byte, name string) ([][]byte, *GRPCError) {
	var out [][]byte

	seen := map[string]bool{}

	var add func(n string) *GRPCError

	add = func(n string) *GRPCError {
		if seen[n] {
			return nil
		}

		seen[n] = true

		fd, err := s.engine.FileDescriptor(raw, n)
		if err != nil {
			return grpcErr(GRPCNotFound, "%v", err)
		}

		out = append(out, fd)

		for _, d := range s.fileImports(raw, n) {
			gerr := add(d)
			if gerr != nil {
				return gerr
			}
		}

		return nil
	}

	gerr := add(name)
	if gerr != nil {
		return nil, gerr
	}

	return out, nil
}

// fileImports lists what a file imports. An engine that can answer directly
// does; for any other, only files that declare a service are covered.
func (s *GRPCServingService) fileImports(raw []byte, name string) []string {
	if imp, ok := s.engine.(ProtoFileImporter); ok {
		deps, err := imp.FileImports(raw, name)
		if err == nil {
			return deps
		}
	}

	svcs, err := s.engine.Services(raw)
	if err != nil {
		return nil
	}

	for _, svc := range svcs {
		n, deps, ferr := s.engine.FileContaining(raw, svc.Name)
		if ferr == nil && n == name {
			return deps
		}
	}

	return nil
}

var errJSONArrayNotStreaming = errors.New("a JSON array is only valid for a server-streaming method")
