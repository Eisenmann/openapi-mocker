// Package mcp implements the adapter-side engine for MCP server manifest
// contracts: parsing/validation, tool listing and JSON-RPC 2.0 request
// execution. The usecase layer defines the port (usecase.MCPEngine); this
// package is the only place that knows the MCP wire format.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// jsonrpcVersion is the only JSON-RPC protocol version spoken by the mock.
const jsonrpcVersion = "2.0"

// JSON-RPC 2.0 error codes (subset used by the mock server).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

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

	for _, v := range usecase.MCPSupportedProtocolVersions {
		if v == p.ProtocolVersion {
			return v
		}
	}

	return usecase.MCPSupportedProtocolVersions[0]
}

// Static manifest validation errors (wrapped with details where useful).
var (
	errNotManifest      = errors.New("no mcpServer block and no tools array: not an MCP server manifest")
	errNoTools          = errors.New("manifest declares no tools, resources or prompts")
	errToolNameRequired = errors.New("tool name is required")
	errDuplicateTool    = errors.New("duplicate tool name")
)

// manifest is the parsed form of an MCP contract. Two layouts are accepted:
//   - mcpServer root: the canonical openapi-mocker layout
//   - flat external layout: serverInfo + tools at the root
type manifest struct {
	Name      string
	Version   string
	Tools     []manifestTool
	Resources []manifestResource
	Templates []manifestResource
	Prompts   []manifestPrompt
}

// manifestTool is one tool entry from the manifest.
type manifestTool struct {
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	InputSchema   json.RawMessage            `json:"inputSchema"`
	Title         string                     `json:"title"`
	OutputSchema  json.RawMessage            `json:"outputSchema"`
	Annotations   json.RawMessage            `json:"annotations"`
	MockResponses map[string]json.RawMessage `json:"mockResponses"`
	// Fault declares fault injection for every call of the tool (see fault.go).
	Fault json.RawMessage `json:"fault"`
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
	Name      string             `json:"name"`
	Version   string             `json:"version"`
	Tools     []manifestTool     `json:"tools"`
	Resources []manifestResource `json:"resources"`
	Templates []manifestResource `json:"resourceTemplates"`
	Prompts   []manifestPrompt   `json:"prompts"`
}

// outerShape probes which layout the raw document uses.
type outerShape struct {
	MCPServer  *mcpServerShape    `json:"mcpServer"`
	ServerInfo *serverInfoShape   `json:"serverInfo"`
	Tools      []manifestTool     `json:"tools"`
	Resources  []manifestResource `json:"resources"`
	Templates  []manifestResource `json:"resourceTemplates"`
	Prompts    []manifestPrompt   `json:"prompts"`
}

// parse decodes and validates a manifest. It returns an error for anything
// the user would consider a broken contract: bad JSON, no tools, duplicate or
// empty tool names, non-object inputSchema.
func parse(raw []byte) (*manifest, error) {
	var outer outerShape

	err := json.Unmarshal(raw, &outer)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	m, ok := outer.manifest()
	if !ok {
		return nil, errNotManifest
	}

	if len(m.Tools)+len(m.Resources)+len(m.Templates)+len(m.Prompts) == 0 {
		return nil, errNoTools
	}

	err = m.validate()
	if err != nil {
		return nil, err
	}

	return m, nil
}

// manifest picks the layout the document uses (false: neither).
func (o *outerShape) manifest() (*manifest, bool) {
	m := &manifest{Name: "", Version: "", Tools: nil, Resources: nil, Templates: nil, Prompts: nil}

	switch {
	case o.MCPServer != nil:
		srv := o.MCPServer
		m.Name, m.Version = srv.Name, srv.Version
		m.Tools, m.Resources, m.Templates, m.Prompts = srv.Tools, srv.Resources, srv.Templates, srv.Prompts
	case o.Tools != nil || o.Resources != nil || o.Templates != nil || o.Prompts != nil:
		if o.ServerInfo != nil {
			m.Name = o.ServerInfo.Name
			m.Version = o.ServerInfo.Version
		}

		m.Tools, m.Resources, m.Templates, m.Prompts = o.Tools, o.Resources, o.Templates, o.Prompts
	default:
		return nil, false
	}

	return m, true
}

// validate checks every part of the manifest.
func (m *manifest) validate() error {
	err := validateTools(m.Tools)
	if err != nil {
		return err
	}

	err = validateResources(m.Resources, m.Templates)
	if err != nil {
		return err
	}

	return validatePrompts(m.Prompts)
}

// checkMatches reports a malformed "match" in any of the tool's responses.
func (t *manifestTool) checkMatches() error {
	for key, raw := range t.MockResponses {
		_, _, err := splitMatch(raw)
		if err != nil {
			return fmt.Errorf("mockResponses.%s: %w", key, err)
		}
	}

	return nil
}

// validateTools checks tool names and mock-response conditions.
func validateTools(tools []manifestTool) error {
	seen := map[string]bool{}

	for i := range tools {
		t := &tools[i]
		if t.Name == "" {
			return fmt.Errorf("tool %d: %w", i, errToolNameRequired)
		}

		if seen[t.Name] {
			return fmt.Errorf("tool %q: %w", t.Name, errDuplicateTool)
		}

		seen[t.Name] = true

		err := t.checkMetadata()
		if err != nil {
			return fmt.Errorf("tool %q: %w", t.Name, err)
		}

		err = t.checkMatches()
		if err != nil {
			return fmt.Errorf("tool %q: %w", t.Name, err)
		}

		err = t.checkFaults()
		if err != nil {
			return fmt.Errorf("tool %q: %w", t.Name, err)
		}
	}

	return nil
}

// Validate reports manifest problems as a structured result (never panics,
// never returns an error - used by the /validate endpoint).
func (e *Engine) Validate(raw []byte) usecase.MCPValidationResult {
	m, err := parse(raw)
	if err != nil {
		return usecase.MCPValidationResult{
			Valid: false, Errors: []string{err.Error()}, ToolCount: 0, ResourceCount: 0, PromptCount: 0,
		}
	}

	return usecase.MCPValidationResult{
		Valid: true, Errors: nil, ToolCount: len(m.Tools),
		ResourceCount: len(m.Resources) + len(m.Templates), PromptCount: len(m.Prompts),
	}
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
	for i := range m.Tools {
		t := &m.Tools[i]

		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}

		tools = append(tools, usecase.MCPTool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			InputSchema:  schema,
			OutputSchema: nonNull(t.OutputSchema),
			Annotations:  nonNull(t.Annotations),
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

	out, err := json.Marshal(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: mustMarshal(result), Error: nil})
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

// respondErrData is respondErr with a structured "data" payload.
func respondErrData(id json.RawMessage, code int, msg string, data any) ([]byte, error) {
	if isNotification(id) {
		return nil, nil
	}

	errObj := &jsonrpcError{Code: code, Message: msg, Data: mustMarshal(data)}

	out, err := json.Marshal(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: nil, Error: errObj})
	if err != nil {
		return nil, fmt.Errorf("marshal error response: %w", err)
	}

	return out, nil
}

// marshalErr builds a JSON-RPC error response with an explicit ID. It skips
// the notification rule: JSON-RPC 2.0 requires a response (with id null) even
// for requests that could not be parsed, where no id could be extracted.
func marshalErr(id json.RawMessage, code int, msg string) ([]byte, error) {
	errObj := &jsonrpcError{Code: code, Message: msg, Data: nil}

	out, err := json.Marshal(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: nil, Error: errObj})
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
	st := &callState{scenario: scenario, validate: false, enforce: false, violations: nil, render: nil, fault: nil}

	return e.run(raw, req, st)
}

// ExecuteWith is the general form of Execute and ExecuteValidated: it
// validates when opts.Validate is set (rejecting invalid calls with
// opts.Enforce) and renders opts.Render over every tool's mock result.
func (e *Engine) ExecuteWith(
	raw, req []byte, opts *usecase.MCPExecOptions,
) (resp []byte, violations []string, err error) {
	st := &callState{
		scenario: opts.Scenario, validate: opts.Validate, enforce: opts.Enforce,
		violations: nil, render: opts.Render, fault: opts.Fault,
	}
	resp, err = e.run(raw, req, st)

	return resp, st.violations, err
}

// ExecuteValidated is Execute with tools/call arguments checked against each
// tool's inputSchema. Violations are returned for every invalid call; with
// enforce set, such a call is answered with a JSON-RPC -32602 error instead
// of the mock response.
func (e *Engine) ExecuteValidated(
	raw, req []byte, scenario string, enforce bool,
) (resp []byte, violations []string, err error) {
	st := &callState{scenario: scenario, validate: true, enforce: enforce, violations: nil, render: nil, fault: nil}
	resp, err = e.run(raw, req, st)

	return resp, st.violations, err
}

// run answers req (a single request or a batch) and records validation
// outcomes in st.
func (e *Engine) run(raw, req []byte, st *callState) ([]byte, error) {
	m, err := parse(raw)
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(req)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return e.executeBatch(m, trimmed, st)
	}

	return e.executeOne(m, trimmed, st)
}

// executeBatch handles a JSON-RPC batch: every element is answered
// independently and the non-notification responses are returned as an array.
func (e *Engine) executeBatch(m *manifest, req []byte, st *callState) ([]byte, error) {
	var items []json.RawMessage

	err := json.Unmarshal(req, &items)
	if err != nil {
		return marshalErr(json.RawMessage("null"), codeParseError, "invalid JSON payload")
	}

	if len(items) == 0 {
		return marshalErr(json.RawMessage("null"), codeInvalidRequest, "empty batch")
	}

	responses := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		out, err := e.executeOne(m, item, st)
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
func (e *Engine) executeOne(m *manifest, req []byte, st *callState) ([]byte, error) {
	var r jsonrpcRequest

	err := json.Unmarshal(req, &r)
	if err != nil {
		return marshalErr(json.RawMessage("null"), codeParseError, "invalid JSON payload")
	}

	if r.JSONRPC != jsonrpcVersion {
		return marshalErr(idOrNull(r.ID), codeInvalidRequest, `invalid request: jsonrpc must be "2.0"`)
	}

	if r.Method == "" {
		return marshalErr(idOrNull(r.ID), codeInvalidRequest, "invalid request: method is required")
	}

	h, ok := methodHandlers[r.Method]
	if !ok {
		return respondErr(r.ID, codeMethodNotFound, "method not found: "+r.Method)
	}

	return h(m, &r, st)
}

// methodHandler answers one JSON-RPC method.
type methodHandler func(m *manifest, r *jsonrpcRequest, st *callState) ([]byte, error)

// methodHandlers maps every supported MCP method to its handler.
var methodHandlers = map[string]methodHandler{
	"initialize": func(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
		return respond(r.ID, initializeResult(m, r.Params))
	},
	"ping": func(_ *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
		return respond(r.ID, map[string]any{})
	},
	"tools/list": func(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
		return respond(r.ID, map[string]any{"tools": m.tools()})
	},
	"tools/call":               callTool,
	"resources/list":           listResources,
	"resources/templates/list": listResourceTemplates,
	"resources/read":           readResource,
	"prompts/list":             listPrompts,
	"prompts/get":              getPrompt,
}

// initializeResult builds the result of the initialize request.
func initializeResult(m *manifest, params json.RawMessage) map[string]any {
	return map[string]any{
		"protocolVersion": negotiateProtocolVersion(params),
		"capabilities":    m.capabilities(),
		"serverInfo": map[string]any{
			"name":    orDefault(m.Name, "mock-mcp-server"),
			"version": orDefault(m.Version, "1.0.0"),
		},
	}
}

// callTool answers a tools/call request from the tool's mock responses.
func callTool(m *manifest, r *jsonrpcRequest, st *callState) ([]byte, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}

	err := json.Unmarshal(r.Params, &p)
	if err != nil || p.Name == "" {
		return respondErr(r.ID, codeInvalidParams, "tools/call requires params.name")
	}

	tool, ok := m.findTool(p.Name)
	if !ok {
		return respondErr(r.ID, codeInvalidParams, "unknown tool: "+p.Name)
	}

	violations := st.check(&tool, p.Name, p.Arguments)
	if st.enforce && len(violations) > 0 {
		return respondErrData(r.ID, codeInvalidParams, "invalid params for tool "+p.Name, violations)
	}

	result, key, ok := tool.resolve(st.scenario, p.Arguments)
	if !ok {
		return respondErr(r.ID, codeInternalError, "no mock response configured for tool: "+p.Name)
	}

	if st.injectsError(&tool, key) {
		return respondErr(r.ID, codeInternalError, "injected failure for chaos testing")
	}

	result = finishResult(&tool, st, result, p.Arguments)

	return respond(r.ID, withValidationWarnings(result, violations))
}

// finishResult renders the placeholders of a tool result and gives it the
// structured content its tool's outputSchema promises.
func finishResult(tool *manifestTool, st *callState, result map[string]any, args json.RawMessage) map[string]any {
	if st.render != nil {
		rendered, isMap := st.render(result, args).(map[string]any)
		if isMap {
			result = rendered
		}
	}

	if len(tool.OutputSchema) > 0 {
		ensureStructured(result)
	}

	return result
}

// findTool returns the tool with the given name.
func (m *manifest) findTool(name string) (manifestTool, bool) {
	for i := range m.Tools {
		if m.Tools[i].Name == name {
			return m.Tools[i], true
		}
	}

	return manifestTool{
		Name: "", Description: "", InputSchema: nil, Title: "", OutputSchema: nil, Annotations: nil,
		MockResponses: nil, Fault: nil,
	}, false
}

// resolve picks the mock result for a tool call and returns it with the key of
// the mockResponses entry it came from. The lookup order
// is: the explicit scenario; then the conditional entry (one with a "match")
// whose conditions all hold for the call's arguments - the one with the most
// conditions wins, ties go to the lexically first key; then "default"; then,
// when no scenario was requested and the tool declares exactly one
// unconditional response, that response. A requested scenario the tool does
// not declare falls through to the same conditional/"default" lookup (like
// REST mock rules), never to an unrelated lone response.
func (t *manifestTool) resolve(scenario string, args json.RawMessage) (res map[string]any, key string, ok bool) {
	if scenario != "" {
		if res, ok = t.entry(scenario); ok {
			return res, scenario, true
		}
	}

	if best, found := t.bestMatch(args); found {
		res, ok = t.entry(best)

		return res, best, ok
	}

	if res, ok = t.entry("default"); ok {
		return res, "default", true
	}

	if scenario == "" {
		return t.loneResponse()
	}

	return nil, "", false
}

// entry decodes the response stored under key, ignoring its conditions.
func (t *manifestTool) entry(key string) (map[string]any, bool) {
	raw, ok := t.MockResponses[key]
	if !ok {
		return nil, false
	}

	_, body, err := splitMatch(raw)
	if err != nil {
		return nil, false
	}

	return decodeMock(body)
}

// bestMatch returns the key of the conditional entry that matches args best.
func (t *manifestTool) bestMatch(args json.RawMessage) (string, bool) {
	keys := make([]string, 0, len(t.MockResponses))
	for k := range t.MockResponses {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	best, bestConds := "", 0

	for _, k := range keys {
		conds, _, err := splitMatch(t.MockResponses[k])
		if err != nil || len(conds) <= bestConds {
			continue
		}

		if matchesArguments(conds, args) {
			best, bestConds = k, len(conds)
		}
	}

	return best, bestConds > 0
}

// loneResponse returns the tool's only unconditional response and its key.
func (t *manifestTool) loneResponse() (res map[string]any, key string, ok bool) {
	count := 0

	for k, raw := range t.MockResponses {
		conds, _, err := splitMatch(raw)
		if err == nil && len(conds) == 0 {
			key, count = k, count+1
		}
	}

	if count != 1 {
		return nil, "", false
	}

	res, ok = t.entry(key)

	return res, key, ok
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

	err := json.Unmarshal(raw, &v)
	if err != nil || v == nil {
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

	res := map[string]any{keyContent: []any{map[string]any{keyType: keyText, keyText: string(b)}}}
	if obj, ok := v.(map[string]any); ok {
		res["structuredContent"] = obj
	}

	return res, true
}

// isCallToolResult reports whether obj already has MCP CallToolResult keys.
func isCallToolResult(obj map[string]any) bool {
	for _, k := range []string{keyContent, "structuredContent", "isError"} {
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
