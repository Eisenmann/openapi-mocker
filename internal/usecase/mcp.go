package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// MCPMockPath is the mock-server path template for the MCP endpoint. It is
// shared by the HTTP router and the request logger so the two stay in sync.
const MCPMockPath = "/mock/%s/mcp"

// ErrNotMCPContract is returned when the project active contract is not an
// MCP server manifest. It wraps ErrUnsupportedFormat so StatusFromError can
// classify it as a client-side problem (HTTP 400).
var ErrNotMCPContract = fmt.Errorf("%w: project is not an MCP contract", ErrUnsupportedFormat)

// MCPValidationResult contains the outcome of validating an MCP manifest.
type MCPValidationResult struct {
	Valid         bool     `json:"valid"`
	Errors        []string `json:"errors,omitempty"`
	ToolCount     int      `json:"toolCount"`
	ResourceCount int      `json:"resourceCount,omitempty"`
	PromptCount   int      `json:"promptCount,omitempty"`
}

// MCPSupportedProtocolVersions lists the MCP protocol revisions the mock
// speaks, newest first. The engine negotiates against it and the HTTP
// transport checks the MCP-Protocol-Version header against it.
var MCPSupportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// MCPTool describes one tool exposed by the mock MCP server: its name,
// description and JSON-Schema input schema (all are returned verbatim in
// tools/list responses, per the MCP specification).
type MCPTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// OutputSchema is the optional JSON Schema of the structured result.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// Annotations are the optional behaviour hints (readOnlyHint, ...).
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// MCPResource is a resource (or resource template, when URITemplate is set)
// exposed by the mock MCP server. The content is never part of the listing.
type MCPResource struct {
	URI         string `json:"uri,omitempty"`
	URITemplate string `json:"uriTemplate,omitempty"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// MCPPromptArgument is one argument of a prompt.
type MCPPromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// MCPPrompt is a prompt exposed by the mock MCP server.
type MCPPrompt struct {
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Arguments   []MCPPromptArgument `json:"arguments,omitempty"`
}

// MCPCatalog is everything a manifest exposes: what tools/list,
// resources/list, resources/templates/list and prompts/list return.
type MCPCatalog struct {
	Tools     []MCPTool     `json:"tools"`
	Resources []MCPResource `json:"resources"`
	Templates []MCPResource `json:"resourceTemplates"`
	Prompts   []MCPPrompt   `json:"prompts"`
}

// MCPEngine is the port that the usecase layer depends on for all MCP work.
// The adapter (implemented with encoding/json, since MCP is JSON-RPC 2.0
// over HTTP) fulfills it. No protocol detail ever crosses this boundary.
// MCPExecOptions tunes MCPEngine.ExecuteWith.
type MCPExecOptions struct {
	Scenario string
	Validate bool
	Enforce  bool
	// Render transforms a tool's decoded mock result (e.g. fills in
	// placeholders), given the tool call's raw arguments.
	Render func(result any, arguments []byte) any
	// Fault, when set, is called for every tools/call whose tool declares
	// fault injection, and returns the fault to inject into that call (""
	// for none). The engine answers a FaultError call with a JSON-RPC
	// error itself; the other faults are applied by the caller.
	Fault func(f *MCPFault) string
}

// MCPFault is the fault injection a tool declares in its manifest: the same
// settings as a REST mock rule (delay and error rate plus the rest of Fault).
type MCPFault struct {
	DelayMs      int
	ErrorRatePct int
	Settings     *domain.Fault
}

type MCPEngine interface {
	Validate(raw []byte) MCPValidationResult
	ParseAndValidate(raw []byte) error
	ListTools(raw []byte) ([]MCPTool, error)
	// Catalog describes the tools, resources, resource templates and prompts
	// of the manifest.
	Catalog(raw []byte) (MCPCatalog, error)
	// ExecuteWith answers a JSON-RPC 2.0 request (or batch) against the
	// manifest. opts.Scenario selects among the tool declared mock responses
	// (empty string means "default") - the same X-Mock-Scenario concept as
	// the plain OpenAPI mock server. With opts.Validate the arguments of every
	// tools/call are checked against the tool's inputSchema and the problems
	// are returned as violations; with opts.Enforce an invalid call is answered
	// with a JSON-RPC invalid-params error (-32602) instead of the mock
	// response. opts.Render, when set, is applied to every mock result.
	//
	// It returns (responseJSON, nil) for a handled request, including
	// JSON-RPC-level errors such as "method not found" or a parse error,
	// which are valid protocol responses with HTTP 200, and (nil, nil) for
	// JSON-RPC notifications, which must not receive a body at all. A non-nil
	// error means the manifest itself failed to load - an infrastructure
	// failure the handler maps to an HTTP error.
	ExecuteWith(raw, req []byte, opts *MCPExecOptions) (resp []byte, violations []string, err error)
}

// MCPServingService serves mock MCP responses for MCP contracts. Like
// GraphQLServingService for GraphQL, it decides "what to respond" (per the
// manifest + JSON-RPC request), not "how to write it to the socket".
type MCPServingService struct {
	contracts ContractRepository
	logs      LogRepository
	engine    MCPEngine
	cfg       servingConfig
	templates *TemplateEngine
}

func NewMCPServingService(
	contracts ContractRepository,
	logs LogRepository,
	engine MCPEngine,
	opts ...ServingOption,
) *MCPServingService {
	return &MCPServingService{
		contracts: contracts, logs: logs, engine: engine, cfg: newServingConfig(opts), templates: NewTemplateEngine(),
	}
}

// MCPResponse is the outcome of serving one MCP request.
type MCPResponse struct {
	// Body is the JSON-RPC response; nil for a notification (no body).
	Body []byte
	// Violations lists tools/call argument problems found by request
	// validation (warn and enforce modes).
	Violations []string
	// Rejected is set when enforce mode answered with an invalid-params error
	// instead of the mock; otherwise the violations were only warnings.
	Rejected bool
	// Status is the HTTP status to answer with (200 unless a fault changed it).
	Status int
	// Fault names the injected fault, if any (see the Fault* constants).
	// FaultDrop, FaultTimeout and FaultTruncated need the HTTP adapter to act
	// on the connection. For a batch, the first faulted call decides.
	Fault string
	// DelayMs is the latency to add before answering (the largest of a
	// batch's calls).
	DelayMs int
	// HoldMs is how long a FaultTimeout request is held before the
	// connection is dropped.
	HoldMs int
}

// Serve handles one JSON-RPC 2.0 request and returns the mock JSON response.
// A nil body with a nil error means the request was a JSON-RPC notification
// (e.g. "notifications/initialized") and must not receive a response body.
func (s *MCPServingService) Serve(projectID string, body []byte, scenario string) ([]byte, error) {
	resp, err := s.Handle(projectID, body, scenario)

	return resp.Body, err
}

// Handle is Serve plus the request-validation outcome. tools/call arguments
// are validated against the tool's inputSchema when the project's validation
// mode is warn or enforce.
func (s *MCPServingService) Handle(projectID string, body []byte, scenario string) (MCPResponse, error) {
	start := time.Now()
	rule := describeMCPRequest(body, scenario)

	raw, err := s.getMCP(projectID)
	if err != nil {
		s.logResult(projectID, &mcpOutcome{status: StatusFromError(err), rule: rule}, start)

		return MCPResponse{}, err
	}

	mode := s.cfg.validationMode(projectID)

	var faults mcpFaultRun

	resp, violations, err := s.execute(projectID, mode, raw, body, scenario, faults.roll)
	if err != nil {
		// Only manifest-level failures reach this point; request-level
		// problems are JSON-RPC error responses produced by the engine.
		s.logResult(projectID, &mcpOutcome{status: StatusInternalServerError, rule: rule}, start)

		return MCPResponse{}, err
	}

	// A notification gets no body (HTTP 202); a JSON-RPC error response is
	// still HTTP 200 but is logged as unmatched so failures are visible.
	status := StatusOK
	if resp == nil {
		status = StatusAccepted
	}

	out := MCPResponse{
		Body:       resp,
		Violations: violations,
		Rejected:   mode == domain.ValidationEnforce && len(violations) > 0,
		Status:     status,
		Fault:      "",
		DelayMs:    faults.delayMs,
		HoldMs:     0,
	}

	faults.apply(&out)

	s.logResult(projectID, &mcpOutcome{
		status: out.Status, rule: rule, matched: !hasJSONRPCError(out.Body), violations: violations, fault: out.Fault,
	}, start)

	return out, nil
}

// execute runs the request through the engine, validating it when the
// project's validation mode asks for that and filling in the mock results'
// {{placeholders}}.
func (s *MCPServingService) execute(
	projectID string, mode domain.ValidationMode, raw string, body []byte, scenario string,
	fault func(f *MCPFault) string,
) (resp []byte, violations []string, err error) {
	return s.engine.ExecuteWith([]byte(raw), body, &MCPExecOptions{
		Scenario: scenario,
		Fault:    fault,
		Validate: mode != domain.ValidationOff,
		Enforce:  mode == domain.ValidationEnforce,
		Render: func(result any, arguments []byte) any {
			return s.templates.RenderValue(result, &TemplateContext{
				ProjectID: projectID, Method: "tools/call", Path: "", PathParams: nil,
				Query: nil, Header: nil, Body: arguments, JSON: false, body: nil, parsed: false,
			})
		},
	})
}

// Tools lists the tools declared by the project MCP manifest.
func (s *MCPServingService) Tools(projectID string) ([]MCPTool, error) {
	raw, err := s.getMCP(projectID)
	if err != nil {
		return nil, err
	}

	return s.engine.ListTools([]byte(raw))
}

// Catalog describes everything the project's MCP manifest exposes.
func (s *MCPServingService) Catalog(projectID string) (MCPCatalog, error) {
	raw, err := s.getMCP(projectID)
	if err != nil {
		return MCPCatalog{}, err
	}

	return s.engine.Catalog([]byte(raw))
}

func (s *MCPServingService) getMCP(projectID string) (string, error) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return "", err
	}

	if c.Format != FormatMCP {
		return "", fmt.Errorf("%w: %s", ErrNotMCPContract, projectID)
	}

	return c.Raw, nil
}

// mcpOutcome is what a served MCP request looked like, for the request log.
type mcpOutcome struct {
	status     int
	rule       string
	matched    bool
	violations []string
	fault      string
}

// logResult records a request log entry (mirrors GraphQLServingService).
func (s *MCPServingService) logResult(projectID string, o *mcpOutcome, start time.Time) {
	s.logs.Add(&domain.RequestLog{
		ID:          "",
		ProjectID:   projectID,
		Method:      methodPost,
		Path:        fmt.Sprintf(MCPMockPath, projectID),
		StatusCode:  o.status,
		MatchedRule: o.rule,
		Matched:     o.matched,
		Timestamp:   time.Now().UTC(),
		DurationMs:  time.Since(start).Milliseconds(),
		Violations:  o.violations,
		Fault:       o.fault,
	})
}

// describeMCPRequest builds the request-log "matched rule" for a JSON-RPC
// body: the method, plus the tool name for tools/call and the requested
// scenario, e.g. "tools/call get_forecast [rainy]". A batch is "batch(n)".
// Unparseable bodies yield an empty description.
func describeMCPRequest(body []byte, scenario string) string {
	trimmed := bytes.TrimSpace(body)

	var desc string

	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []json.RawMessage
		err := json.Unmarshal(trimmed, &items)
		if err != nil {
			return ""
		}

		desc = fmt.Sprintf("batch(%d)", len(items))
	} else {
		var r struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		err := json.Unmarshal(trimmed, &r)
		if err != nil || r.Method == "" {
			return ""
		}

		desc = r.Method
		if r.Method == "tools/call" && r.Params.Name != "" {
			desc += " " + r.Params.Name
		}
	}

	if scenario != "" {
		desc += " [" + scenario + "]"
	}

	return desc
}

// hasJSONRPCError reports whether a JSON-RPC response (or any element of a
// batch response) is an error object.
func hasJSONRPCError(resp []byte) bool {
	trimmed := bytes.TrimSpace(resp)
	if len(trimmed) == 0 {
		return false
	}

	var items []json.RawMessage
	if trimmed[0] == '[' {
		err := json.Unmarshal(trimmed, &items)
		if err != nil {
			return true
		}
	} else {
		items = []json.RawMessage{trimmed}
	}

	for _, item := range items {
		var r struct {
			Error json.RawMessage `json:"error"`
		}
		err := json.Unmarshal(item, &r)
		if err == nil && len(r.Error) > 0 && string(r.Error) != "null" {
			return true
		}
	}

	return false
}
