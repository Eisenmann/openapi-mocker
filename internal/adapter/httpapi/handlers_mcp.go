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

	defer r.Body.Close()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	result, err := a.s.MCPServing.Handle(r.PathValue("projectId"), body, r.Header.Get("X-Mock-Scenario"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	// Request validation outcome, visible to HTTP-level tooling (the same
	// header the REST mock uses). Warnings are also in the result's _meta.
	if len(result.Violations) > 0 {
		verdict := "warn"
		if result.Rejected {
			verdict = "rejected"
		}

		w.Header().Set("X-Mock-Validation", fmt.Sprintf("%s: %d violation(s)", verdict, len(result.Violations)))
	}

	// JSON-RPC notifications must not receive a response body; the MCP
	// Streamable HTTP transport requires 202 Accepted for them.
	if result.Body == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Body)
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
