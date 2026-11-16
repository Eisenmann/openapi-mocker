// Package mcpcontrol is the MCP control plane: an MCP server whose tools let an
// agent (in an editor, in CI) create projects, import contracts, add mock rules
// and faults, read the request log, verify calls and run load tests, without a
// person touching the web UI.
//
// It is an interface adapter, like internal/adapter/httpapi: it speaks the
// protocol (JSON-RPC 2.0 over any transport) and calls usecase services. It is
// not the mock MCP server, which serves a contract's tools to the system under
// test (internal/adapter/mcp).
package mcpcontrol

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

const (
	serverName = "openapi-mocker-control"
	jsonRPC    = "2.0"
)

// instructions is shown to the model when it connects.
const instructions = `OpenAPI Mocker control plane. Work in this order:
1. create_project with a contract (OpenAPI YAML/JSON, GraphQL SDL, MCP manifest or .proto),
   or import_contract into an existing project.
2. The project is served at its mock_url at once; call the endpoints there from the system under test.
3. add_mock_rule for fixed responses and set_fault for latency, errors and broken responses.
4. verify_calls to assert what the system sent to the mock (method, path, query, body, count);
   get_logs to see the calls.
5. run_load_test and get_load_run for load tests against the mock.
A project can be named by its id or its name. Tool errors say what to fix.`

// Server answers MCP requests with the control-plane tools.
type Server struct {
	deps  Deps
	tools []*tool
	index map[string]*tool
}

// New builds a server over the given services.
func New(deps *Deps) *Server {
	s := &Server{deps: *deps, tools: nil, index: map[string]*tool{}}

	s.tools = s.buildTools()
	for _, t := range s.tools {
		s.index[t.name] = t
	}

	return s
}

// ToolNames lists the tools the server offers, in order.
func (s *Server) ToolNames() []string {
	names := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		names = append(names, t.name)
	}

	return names
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handle answers one JSON-RPC message. It returns nil for a notification,
// which gets no reply.
func (s *Server) Handle(ctx context.Context, line []byte) []byte {
	var req rpcRequest

	err := json.Unmarshal(line, &req)
	if err != nil {
		return encode(&rpcResponse{
			JSONRPC: jsonRPC, ID: json.RawMessage(jsonNull), Result: nil,
			Error: &rpcError{Code: codeParse, Message: "parse error: " + err.Error()},
		})
	}

	notification := len(req.ID) == 0 || string(req.ID) == jsonNull

	if req.JSONRPC != jsonRPC || req.Method == "" {
		if notification {
			return nil
		}

		return fail(req.ID, codeInvalidRequest, "not a JSON-RPC 2.0 request")
	}

	result, rpcErr := s.dispatch(ctx, &req)

	if notification {
		return nil
	}

	if rpcErr != nil {
		return fail(req.ID, rpcErr.Code, rpcErr.Message)
	}

	return encode(&rpcResponse{JSONRPC: jsonRPC, ID: req.ID, Result: result, Error: nil})
}

func (s *Server) dispatch(ctx context.Context, req *rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.list(), nil
	case "tools/call":
		return s.call(ctx, req.Params)
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil, nil //nolint:nilnil // a notification has neither result nor error.
		}

		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}

	_ = json.Unmarshal(params, &p)

	version := usecase.MCPSupportedProtocolVersions[0]
	if slices.Contains(usecase.MCPSupportedProtocolVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}

	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{keyName: serverName, keyVersion: s.deps.Version},
		"instructions":    instructions,
	}
}

func (s *Server) list() any {
	out := make([]map[string]any, 0, len(s.tools))

	for _, t := range s.tools {
		out = append(out, map[string]any{
			keyName:        t.name,
			keyDescription: t.description,
			"inputSchema":  t.schema,
			"annotations": map[string]any{
				"title":           t.title,
				"readOnlyHint":    t.readOnly,
				"destructiveHint": t.destructive,
				"idempotentHint":  t.readOnly,
				"openWorldHint":   false,
			},
		})
	}

	return map[string]any{"tools": out}
}

func (s *Server) call(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}

	err := json.Unmarshal(params, &p)
	if err != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a tool name"}
	}

	t, ok := s.index[p.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
	}

	args := p.Arguments
	if len(args) == 0 || string(args) == jsonNull {
		args = json.RawMessage("{}")
	}

	value, err := t.run(ctx, args)
	if err != nil {
		return toolError(err.Error()), nil
	}

	return toolResult(value), nil
}

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"cannot encode the response"}}`)
	}

	return b
}

func fail(id json.RawMessage, code int, message string) []byte {
	return encode(&rpcResponse{JSONRPC: jsonRPC, ID: id, Result: nil, Error: &rpcError{Code: code, Message: message}})
}

// toolResult wraps a tool's result: the JSON as text, which every client
// shows to the model, and as structured content for clients that read it.
func toolResult(value any) map[string]any {
	text, err := json.Marshal(value)
	if err != nil {
		return toolError("cannot encode the result: " + err.Error())
	}

	return map[string]any{
		"content":           []map[string]any{{keyType: keyText, keyText: string(text)}},
		"structuredContent": value,
		"isError":           false,
	}
}

// toolError reports a failed tool call in the result, where the model sees it
// and can fix the call, rather than as a protocol error.
func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{keyType: keyText, keyText: message}},
		"isError": true,
	}
}
