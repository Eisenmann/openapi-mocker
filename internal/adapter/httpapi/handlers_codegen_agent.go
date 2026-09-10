package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

// codegenAgentRequest is the HTTP request body for the multi-language
// code generation endpoint.
type codegenAgentRequest struct {
	Languages   []string `json:"languages"`
	ProjectName string   `json:"project_name"`
	Strategy    string   `json:"strategy,omitempty"`
}

// codegenAgentResponse is the HTTP response for the multi-language
// code generation endpoint.
type codegenAgentResponse struct {
	Results []codegenResultSummary `json:"results"`
	Error   string                 `json:"error,omitempty"`
}

// codegenResultSummary is a compact summary of a generation result.
type codegenResultSummary struct {
	Language string   `json:"language"`
	Strategy string   `json:"strategy"`
	Files    []string `json:"files"`
	Warnings []string `json:"warnings,omitempty"`
}

// codegenAgent handles POST /api/projects/{id}/codegen/agent.
// It runs the multi-language code generation agent for the project's
// active contract and returns a zip archive of all generated files.
func (a *api) codegenAgent(w http.ResponseWriter, r *http.Request) {
	if a.s.CodeGenAgent == nil {
		writeError(w, http.StatusServiceUnavailable, errCodeGenAgentNotConfigured)

		return
	}

	projectID := r.PathValue("id")

	var req codegenAgentRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidJSON)

		return
	}

	// Load the active contract for the project.
	contract, err := a.s.Contracts.GetActive(projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, errContractNotLoaded)

			return
		}

		writeError(w, http.StatusUnprocessableEntity, err)

		return
	}

	agentReq := buildAgentRequest(req, contract)

	// Execute the agent with a generous timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	results, err := a.s.CodeGenAgent.Execute(ctx, agentReq)
	if err != nil {
		// Partial failure: still return the results that succeeded.
		if len(results) == 0 {
			writeError(w, http.StatusUnprocessableEntity, err)

			return
		}
	}

	// Build a zip archive from all generated files.
	zipBytes, zipErr := buildCodegenZip(results)
	if zipErr != nil {
		writeError(w, http.StatusInternalServerError, zipErr)

		return
	}

	// Attach a summary as a manifest file inside the zip.
	manifest := buildCodegenManifest(results, err)

	zipBytes, manifestErr := appendManifestToZip(zipBytes, manifest)
	if manifestErr != nil {
		writeError(w, http.StatusInternalServerError, manifestErr)

		return
	}

	writeZip(w, "codegen.zip", zipBytes)
}

// buildCodegenZip packs all generated files into a zip archive.
func buildCodegenZip(results []*ports.GenerationResult) ([]byte, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	for _, result := range results {
		for _, f := range result.Files {
			name := string(result.Language) + "/" + f.Path

			w, err := zw.Create(name)
			if err != nil {
				return nil, err
			}

			if _, err := w.Write(f.Content); err != nil {
				return nil, err
			}
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// buildCodegenManifest creates a JSON manifest describing the generation
// results, including any partial-failure error.
func buildCodegenManifest(results []*ports.GenerationResult, genErr error) []byte {
	resp := codegenAgentResponse{
		Results: make([]codegenResultSummary, 0, len(results)),
		Error:   "",
	}

	for _, result := range results {
		resp.Results = append(resp.Results, summarizeResult(result))
	}

	if genErr != nil {
		resp.Error = genErr.Error()
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}

	return data
}

// appendManifestToZip adds a manifest.json entry to an existing zip archive
// and returns the new zip bytes.
func appendManifestToZip(zipBytes, manifest []byte) ([]byte, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	// Copy existing entries.
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		w, err := zw.Create(f.Name)
		if err != nil {
			rc.Close()

			return nil, err
		}

		content, err := io.ReadAll(rc)
		if err != nil {
			rc.Close()

			return nil, err
		}

		if _, err := w.Write(content); err != nil {
			rc.Close()

			return nil, err
		}

		rc.Close()
	}

	// Add manifest.
	mw, err := zw.Create("manifest.json")
	if err != nil {
		return nil, err
	}

	if _, err := mw.Write(manifest); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// buildAgentRequest converts the HTTP request and contract into an agent request.
func buildAgentRequest(req codegenAgentRequest, contract *domain.Contract) *agents.AgentRequest {
	languages := make([]ports.Language, 0, len(req.Languages))
	for _, lang := range req.Languages {
		languages = append(languages, ports.Language(lang))
	}

	return &agents.AgentRequest{
		Languages:   languages,
		Schema:      buildSchemaFromContract(contract),
		ProjectName: req.ProjectName,
		OutputBase:  "./out",
		Strategy:    ports.GenerationStrategy(req.Strategy),
	}
}

// buildSchemaFromContract converts a stored contract into an OpenAPI schema.
func buildSchemaFromContract(contract *domain.Contract) *ports.OpenAPISchema {
	return &ports.OpenAPISchema{
		Content:     []byte(contract.Raw),
		Format:      contract.Format,
		Version:     "",
		Definitions: map[string]interface{}{},
	}
}

// summarizeResult converts a generation result into a compact HTTP summary.
func summarizeResult(result *ports.GenerationResult) codegenResultSummary {
	files := make([]string, 0, len(result.Files))
	for _, f := range result.Files {
		files = append(files, f.Path)
	}

	return codegenResultSummary{
		Language: string(result.Language),
		Strategy: string(result.Strategy),
		Files:    files,
		Warnings: result.Warnings,
	}
}
