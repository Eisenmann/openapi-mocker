package httpapi

import (
	"fmt"
	"io"
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// maxMCPBodyBytes caps the accepted JSON-RPC request body size.
const maxMCPBodyBytes = 1 << 20

// serveMCP answers one JSON-RPC 2.0 request (POST /mock/{projectId}/mcp).
// The whole request body is forwarded to the MCP serving service. Scenario
// selection mirrors the OpenAPI mock server: the X-Mock-Scenario header
// chooses among the tool declared mock responses.
func (a *api) serveMCP(w http.ResponseWriter, r *http.Request) {
	if a.s.MCPServing == nil {
		writeError(w, http.StatusNotFound, errMCPNotEnabled)
		return
	}

	if a.guardEndpoint(w, r) {
		return
	}

	defer r.Body.Close()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	project := r.PathValue("projectId")
	initialize := rpcMethod(body) == methodInitialize

	if _, ok := a.checkTransport(w, r, initialize); !ok {
		return
	}

	result, err := a.s.MCPServing.Handle(project, body, r.Header.Get("X-Mock-Scenario"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	if injectConnectionFault(w, r, result.DelayMs, result.Fault, result.HoldMs) {
		return
	}

	setValidationHeader(w, &result)

	if initialize && result.Body != nil && hasResult(result.Body) {
		w.Header().Set(headerMCPSession, a.sessions.start(project))
	}

	writeMCPBody(w, r, body, &result)
}

// writeMCPBody writes the JSON-RPC answer: nothing but 202 for a
// notification, an event stream when the client wants one, JSON otherwise.
func writeMCPBody(w http.ResponseWriter, r *http.Request, request []byte, result *usecase.MCPResponse) {
	// JSON-RPC notifications must not receive a response body; the MCP
	// Streamable HTTP transport requires 202 Accepted for them.
	if result.Body == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if result.Fault != "" {
		w.Header().Set("X-Mock-Fault", result.Fault)
	}

	if result.Fault != usecase.FaultTruncated && wantsSSE(r) {
		writeSSE(w, result.Status, request, result.Body)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if result.Fault == usecase.FaultTruncated {
		writeTruncated(w, result.Status, result.Body)

		return
	}

	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}

// setValidationHeader reports the request validation outcome to HTTP-level
// tooling (the same header the REST mock uses). Warnings are also in the
// result's _meta.
func setValidationHeader(w http.ResponseWriter, result *usecase.MCPResponse) {
	if len(result.Violations) == 0 {
		return
	}

	verdict := "warn"
	if result.Rejected {
		verdict = "rejected"
	}

	w.Header().Set("X-Mock-Validation", fmt.Sprintf("%s: %d violation(s)", verdict, len(result.Violations)))
}

// mcpTools lists the tools declared by the project MCP manifest - the same
// metadata tools/list returns, exposed as REST for the web UI.
func (a *api) mcpTools(w http.ResponseWriter, r *http.Request) {
	if a.s.MCPServing == nil {
		writeError(w, http.StatusNotFound, errMCPNotEnabled)
		return
	}

	tools, err := a.s.MCPServing.Tools(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

// mcpCatalog describes everything the project MCP manifest exposes: tools,
// resources, resource templates and prompts (for the web UI).
func (a *api) mcpCatalog(w http.ResponseWriter, r *http.Request) {
	if a.s.MCPServing == nil {
		writeError(w, http.StatusNotFound, errMCPNotEnabled)
		return
	}

	cat, err := a.s.MCPServing.Catalog(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, cat)
}
