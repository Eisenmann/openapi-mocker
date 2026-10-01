// Package mcp implements the adapter-side engine for MCP server manifest
// contracts: parsing/validation, tool listing and JSON-RPC 2.0 request
// execution. The usecase layer defines the port (usecase.MCPEngine); this
// package is the only place that knows the MCP wire format.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// JSON-RPC 2.0 error codes (subset used by the mock server).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// supportedProtocolVersions lists the MCP protocol revisions the mock speaks,
// newest first. The mock only implements the tools surface, which is wire
// compatible across these revisions.
var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// negotiateProtocolVersion echoes the client's requested version when it is
// supported, otherwise answers with the newest version the mock supports
// (per the MCP lifecycle spec the client then decides whether to continue).
func negotiateProtocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	for _, v := range supportedProtocolVersions {
		if v == p.ProtocolVersion {
			return v
		}
	}

	return supportedProtocolVersions[0]
}

// manifest is the parsed form of an MCP contract. Two layouts are accepted:
//   - mcpServer root: the canonical openapi-mocker layout
//   - flat external layout: serverInfo + tools at the root
type manifest struct {
	Name    string
	Version string
	Tools   []manifestTool
}

// manifestTool is one tool entry from the manifest.
type manifestTool struct {
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	InputSchema   json.RawMessage            `json:"inputSchema"`
	MockResponses map[string]json.RawMessage `json:"mockResponses"`
}

// Engine fulfills the usecase.MCPEngine port.
type Engine struct{}

// NewEngine returns a stateless MCP engine.
func NewEngine() *Engine {
	return &Engine{}
}

// serverInfoShape is the external-layout serverInfo block.
type serverInfoShape struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// mcpServerShape is the canonical openapi-mocker layout.
type mcpServerShape struct {
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Tools   []manifestTool `json:"tools"`
}

// outerShape probes which layout the raw document uses.
type outerShape struct {
	MCPServer  *mcpServerShape  `json:"mcpServer"`
	ServerInfo *serverInfoShape `json:"serverInfo"`
	Tools      []manifestTool   `json:"tools"`
}

// parse decodes and validates a manifest. It returns an error for anything
// the user would consider a broken contract: bad JSON, no tools, duplicate or
// empty tool names, non-object inputSchema.
func parse(raw []byte) (*manifest, error) {
	var outer outerShape
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	m := &manifest{}
	switch {
	case outer.MCPServer != nil:
		m.Name = outer.MCPServer.Name
		m.Version = outer.MCPServer.Version
		m.Tools = outer.MCPServer.Tools
	case outer.Tools != nil:
		m.Name = ""
		if outer.ServerInfo != nil {
			m.Name = outer.ServerInfo.Name
			m.Version = outer.ServerInfo.Version
		}
		m.Tools = outer.Tools
	default:
		return nil, fmt.Errorf("no mcpServer block and no tools array: not an MCP server manifest")
	}

	if len(m.Tools) == 0 {
		return nil, fmt.Errorf("manifest declares no tools")
	}

	seen := map[string]bool{}
	for i, t := range m.Tools {
		if t.Name == "" {
			return nil, fmt.Errorf("tool %d: name is required", i)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("tool %q: duplicate name", t.Name)
		}
		seen[t.Name] = true
	}

	return m, nil
}

// Validate reports manifest problems as a structured result (never panics,
// never returns an error - used by the /validate endpoint).
func (e *Engine) Validate(raw []byte) usecase.MCPValidationResult {
	m, err := parse(raw)
	if err != nil {
		return usecase.MCPValidationResult{Valid: false, Errors: []string{err.Error()}}
	}

	return usecase.MCPValidationResult{Valid: true, ToolCount: len(m.Tools)}
}

// ParseAndValidate is the strict variant used at publish time.
func (e *Engine) ParseAndValidate(raw []byte) error {
	_, err := parse(raw)
	return err
}

// ListTools returns the tool metadata served by tools/list.
func (e *Engine) ListTools(raw []byte) ([]usecase.MCPTool, error) {
	m, err := parse(raw)
	if err != nil {
		return nil, err
	}

	return m.tools(), nil
}

// tools converts the manifest tools into the tools/list representation.
func (m *manifest) tools() []usecase.MCPTool {
	tools := make([]usecase.MCPTool, 0, len(m.Tools))
	for _, t := range m.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, usecase.MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	return tools
}

// jsonrpcRequest is one parsed JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// jsonrpcResponse is one JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// isNotification reports whether the request is a notification (no id:
// JSON-RPC 2.0 forbids replying to notifications).
func isNotification(id json.RawMessage) bool {
	return len(id) == 0 || string(id) == "null"
}

// idOrNull returns the request id, or the JSON null literal when absent.
func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}

	return id
}

// respond marshals a success response, or nil for a notification.
func respond(id json.RawMessage, result any) ([]byte, error) {
	if isNotification(id) {
		return nil, nil
	}
	out, err := json.Marshal(jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: mustMarshal(result)})
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return out, nil
}

// respondErr marshals an error response, or nil for a notification.
func respondErr(id json.RawMessage, code int, msg string) ([]byte, error) {
	if isNotification(id) {
		return nil, nil
	}

	return marshalErr(id, code, msg)
}

// marshalErr builds a JSON-RPC error response with an explicit ID. It skips
// the notification rule: JSON-RPC 2.0 requires a response (with id null) even
// for requests that could not be parsed, where no id could be extracted.
func marshalErr(id json.RawMessage, code int, msg string) ([]byte, error) {
	errObj := &jsonrpcError{Code: code, Message: msg}
	out, err := json.Marshal(jsonrpcResponse{JSONRPC: "2.0", ID: id, Error: errObj})
	if err != nil {
		return nil, fmt.Errorf("marshal error response: %w", err)
	}

	return out, nil
}

// mustMarshal never fails for the shapes used here (maps/slices/strings).
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// Execute answers one JSON-RPC 2.0 request (or a JSON-RPC batch: an array of
// requests) per the mock manifest. Protocol-level failures (unknown method,
// bad params) come back as valid JSON-RPC error objects with HTTP 200. Only
// manifest load failures return a Go error (the caller maps those to an HTTP
// status). Notifications produce no output: (nil, nil), and a batch made only
// of notifications also produces none.
func (e *Engine) Execute(raw, req []byte, scenario string) ([]byte, error) {
	m, err := parse(raw)
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(req)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return e.executeBatch(m, trimmed, scenario)
	}

	return e.executeOne(m, trimmed, scenario)
}

// executeBatch handles a JSON-RPC batch: every element is answered
// independently and the non-notification responses are returned as an array.
func (e *Engine) executeBatch(m *manifest, req []byte, scenario string) ([]byte, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(req, &items); err != nil {
		return marshalErr(json.RawMessage("null"), codeParseError, "invalid JSON payload")
	}
	if len(items) == 0 {
		return marshalErr(json.RawMessage("null"), codeInvalidRequest, "empty batch")
	}

	responses := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		out, err := e.executeOne(m, item, scenario)
		if err != nil {
			return nil, err
		}
		if out != nil {
			responses = append(responses, out)
		}
	}
	if len(responses) == 0 {
		return nil, nil
	}

	out, err := json.Marshal(responses)
	if err != nil {
		return nil, fmt.Errorf("marshal batch response: %w", err)
	}

	return out, nil
}

// executeOne answers a single JSON-RPC request object.
func (e *Engine) executeOne(m *manifest, req []byte, scenario string) ([]byte, error) {
	var r jsonrpcRequest
	if err := json.Unmarshal(req, &r); err != nil {
		return marshalErr(json.RawMessage("null"), codeParseError, "invalid JSON payload")
	}
	if r.JSONRPC != "2.0" {
		return marshalErr(idOrNull(r.ID), codeInvalidRequest, `invalid request: jsonrpc must be "2.0"`)
	}
	if r.Method == "" {
		return marshalErr(idOrNull(r.ID), codeInvalidRequest, "invalid request: method is required")
	}

	switch r.Method {
	case "initialize":
		return respond(r.ID, map[string]any{
			"protocolVersion": negotiateProtocolVersion(r.Params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": orDefault(m.Name, "mock-mcp-server"), "version": orDefault(m.Version, "1.0.0")},
		})
	case "ping":
		return respond(r.ID, map[string]any{})
	case "tools/list":
		return respond(r.ID, map[string]any{"tools": m.tools()})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return respondErr(r.ID, codeInvalidParams, "tools/call requires params.name")
		}
		if p.Name == "" {
			return respondErr(r.ID, codeInvalidParams, "tools/call requires params.name")
		}
		tool, ok := m.findTool(p.Name)
		if !ok {
			return respondErr(r.ID, codeInvalidParams, "unknown tool: "+p.Name)
		}
		result, ok := tool.mockResponse(scenario)
		if !ok {
			return respondErr(r.ID, codeInternalError, "no mock response configured for tool: "+p.Name)
		}
		return respond(r.ID, result)
	default:
		return respondErr(r.ID, codeMethodNotFound, "method not found: "+r.Method)
	}
}

// findTool returns the tool with the given name.
func (m *manifest) findTool(name string) (manifestTool, bool) {
	for _, t := range m.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return manifestTool{}, false
}

// mockResponse resolves the mock result for the scenario. The lookup order
// is: the explicit scenario, then "default". When no scenario was requested
// and the tool declares exactly one response without a "default" key, that
// single response is used. A requested scenario the tool does not declare
// falls back to "default" only (like REST mock rules), never to an
// unrelated lone response.
func (t *manifestTool) mockResponse(scenario string) (map[string]any, bool) {
	if scenario != "" {
		if raw, ok := t.MockResponses[scenario]; ok {
			if res, ok := decodeMock(raw); ok {
				return res, true
			}
		}
	}
	if raw, ok := t.MockResponses["default"]; ok {
		if res, ok := decodeMock(raw); ok {
			return res, true
		}
	}
	if scenario == "" && len(t.MockResponses) == 1 {
		for _, raw := range t.MockResponses {
			return decodeMock(raw)
		}
	}
	return nil, false
}

// decodeMock converts a stored mock value into a tools/call result object.
// An object that already looks like an MCP CallToolResult (it has "content",
// "structuredContent" or "isError") is used verbatim. Any other JSON value is
// wrapped into a valid result: its JSON text becomes a text content item and,
// for objects, it is also exposed as structuredContent.
func decodeMock(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		return nil, false
	}
	if obj, ok := v.(map[string]any); ok {
		if isCallToolResult(obj) {
			return obj, true
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	res := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}}
	if obj, ok := v.(map[string]any); ok {
		res["structuredContent"] = obj
	}

	return res, true
}

// isCallToolResult reports whether obj already has MCP CallToolResult keys.
func isCallToolResult(obj map[string]any) bool {
	for _, k := range []string{"content", "structuredContent", "isError"} {
		if _, ok := obj[k]; ok {
			return true
		}
	}

	return false
}

// orDefault returns fallback when s is empty.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
