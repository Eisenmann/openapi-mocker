package httpapi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// mockCodeGenAgent is a fake agents.CodeGenerationAgent for testing.
type mockCodeGenAgent struct {
	results []*ports.GenerationResult
	err     error
}

func (m *mockCodeGenAgent) Execute(ctx context.Context, request *agents.AgentRequest) ([]*ports.GenerationResult, error) {
	return m.results, m.err
}

// newTestRouterWithAgent creates a test router with a mock codegen agent.
func newTestRouterWithAgent(t *testing.T, agent *mockCodeGenAgent) http.Handler {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	mocks := newMemMockRepo()
	providers := newMemProviderRepo()
	logs := newMemLogRepo()
	engine := &stubEngine{}
	llm := &stubLLM{}
	codegen := &stubCodegen{}
	gqlEngine := &stubGraphQLEngine{}

	svc := httpapi.Services{
		Projects:       usecase.NewProjectService(projects),
		Contracts:      usecase.NewContractService(contracts, providers, engine, llm),
		Mocks:          usecase.NewMockService(mocks, contracts, providers, engine, llm),
		Providers:      usecase.NewProviderService(providers, llm),
		MockServing:    usecase.NewMockServingService(contracts, mocks, logs, engine),
		GraphQLServing: usecase.NewGraphQLServingService(contracts, logs, gqlEngine),
		Codegen:        usecase.NewCodegenService(contracts, codegen),
		Logs:           usecase.NewLogService(logs),
		CodeGenAgent:   agent,
	}

	return httpapi.NewRouter(&svc)
}

// readZipManifest extracts and decodes manifest.json from a zip response.
func readZipManifest(t *testing.T, rec *httptest.ResponseRecorder) codegenAgentResponse {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("expected zip response, got error: %v", err)
	}

	var manifest []byte
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("failed to open manifest: %v", err)
			}

			manifest, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("failed to read manifest: %v", err)
			}

			break
		}
	}

	if manifest == nil {
		t.Fatal("manifest.json not found in zip")
	}

	var resp codegenAgentResponse
	if err := json.Unmarshal(manifest, &resp); err != nil {
		t.Fatalf("failed to decode manifest: %v", err)
	}

	return resp
}

// zipFileNames returns the list of file names in a zip response.
func zipFileNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("expected zip response, got error: %v", err)
	}

	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	return names
}

func TestRouter_CodegenAgent(t *testing.T) {
	t.Parallel()

	agent := &mockCodeGenAgent{
		results: []*ports.GenerationResult{
			{
				Files: []ports.GeneratedFile{
					{Path: "./out/go/server/server_interface.go", Content: []byte("package api")},
					{Path: "./out/go/client/client.go", Content: []byte("package api")},
				},
				Language: ports.LanguageGo,
				Strategy: ports.StrategyNative,
			},
			{
				Files: []ports.GeneratedFile{
					{Path: "./out/ts/client.ts", Content: []byte("// generated")},
				},
				Language: ports.LanguageTypeScript,
				Strategy: ports.StrategyOpenAPI,
				Warnings: []string{"warning 1"},
			},
		},
	}

	h := newTestRouterWithAgent(t, agent)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add contract via API.
	contractBody, _ := json.Marshal(map[string]string{"raw": validOpenAPI})
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", bytes.NewReader(contractBody), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Execute codegen agent.
	body := `{"languages":["go","typescript"],"project_name":"test"}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify zip content type.
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("expected application/zip, got %s", ct)
	}

	// Verify files are in the zip.
	names := zipFileNames(t, rec)
	expected := []string{
		"go/./out/go/server/server_interface.go",
		"go/./out/go/client/client.go",
		"typescript/./out/ts/client.ts",
		"manifest.json",
	}
	for _, want := range expected {
		found := false
		for _, got := range names {
			if got == want {
				found = true

				break
			}
		}
		if !found {
			t.Errorf("expected %q in zip, got %v", want, names)
		}
	}

	// Verify manifest.
	resp := readZipManifest(t, rec)

	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}

	if resp.Results[0].Language != "go" {
		t.Errorf("expected go, got %s", resp.Results[0].Language)
	}
	if resp.Results[0].Strategy != "native" {
		t.Errorf("expected native, got %s", resp.Results[0].Strategy)
	}
	if len(resp.Results[0].Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(resp.Results[0].Files))
	}

	if resp.Results[1].Language != "typescript" {
		t.Errorf("expected typescript, got %s", resp.Results[1].Language)
	}
	if len(resp.Results[1].Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(resp.Results[1].Warnings))
	}
}

func TestRouter_CodegenAgent_InvalidJSON(t *testing.T) {
	t.Parallel()

	agent := &mockCodeGenAgent{}
	h := newTestRouterWithAgent(t, agent)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Invalid JSON body.
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(`{invalid`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestRouter_CodegenAgent_MissingContract(t *testing.T) {
	t.Parallel()

	agent := &mockCodeGenAgent{}
	h := newTestRouterWithAgent(t, agent)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// No contract saved.
	body := `{"languages":["go"],"project_name":"test"}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "contract has not been loaded") {
		t.Errorf("expected clear contract error, got: %s", rec.Body.String())
	}
}

func TestRouter_CodegenAgent_AgentError(t *testing.T) {
	t.Parallel()

	agent := &mockCodeGenAgent{err: errAgentFailure}
	h := newTestRouterWithAgent(t, agent)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add contract via API.
	contractBody, _ := json.Marshal(map[string]string{"raw": validOpenAPI})
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", bytes.NewReader(contractBody), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Execute codegen agent - should fail with 422.
	body := `{"languages":["go"],"project_name":"test"}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rec.Code)
	}
}

func TestRouter_CodegenAgent_PartialFailure(t *testing.T) {
	t.Parallel()

	agent := &mockCodeGenAgent{
		results: []*ports.GenerationResult{
			{
				Files: []ports.GeneratedFile{
					{Path: "./out/ts/client.ts", Content: []byte("// generated")},
				},
				Language: ports.LanguageTypeScript,
				Strategy: ports.StrategyOpenAPI,
			},
		},
		err: errAgentFailure,
	}
	h := newTestRouterWithAgent(t, agent)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add contract via API.
	contractBody, _ := json.Marshal(map[string]string{"raw": validOpenAPI})
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", bytes.NewReader(contractBody), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Execute codegen agent - partial failure should still return 200 with zip.
	body := `{"languages":["go","typescript"],"project_name":"test"}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify manifest has error.
	resp := readZipManifest(t, rec)

	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}

	if resp.Error == "" {
		t.Error("expected error message in manifest")
	}
}

func TestRouter_CodegenAgent_NotConfigured(t *testing.T) {
	t.Parallel()

	// Router without CodeGenAgent.
	h, _, _, _, _, _, _, _, _, _ := newTestRouter(t)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	body := `{"languages":["go"],"project_name":"test"}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestRouter_CodegenAgent_WithStrategy(t *testing.T) {
	t.Parallel()

	agent := &mockCodeGenAgent{
		results: []*ports.GenerationResult{
			{
				Files: []ports.GeneratedFile{
					{Path: "./out/go/server/server_interface.go", Content: []byte("package api")},
				},
				Language: ports.LanguageGo,
				Strategy: ports.StrategyNative,
			},
		},
	}
	h := newTestRouterWithAgent(t, agent)

	// Create project.
	rec := doRequest(t, h, "POST", "/api/projects", strings.NewReader(`{"name":"P"}`), map[string]string{"Content-Type": "application/json"})

	var p domain.Project
	decodeJSON(t, rec, &p)

	// Add contract via API.
	contractBody, _ := json.Marshal(map[string]string{"raw": validOpenAPI})
	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/contract", bytes.NewReader(contractBody), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Execute with strategy.
	body := `{"languages":["go"],"project_name":"test","strategy":"native"}`

	rec = doRequest(t, h, "POST", "/api/projects/"+p.ID+"/codegen/agent", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify manifest.
	resp := readZipManifest(t, rec)

	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
}

// codegenAgentResponse mirrors the internal response type for decoding.
type codegenAgentResponse struct {
	Results []codegenResultSummary `json:"results"`
	Error   string                 `json:"error,omitempty"`
}

type codegenResultSummary struct {
	Language string   `json:"language"`
	Strategy string   `json:"strategy"`
	Files    []string `json:"files"`
	Warnings []string `json:"warnings,omitempty"`
}

var errAgentFailure = &agentError{msg: "agent failure"}

type agentError struct{ msg string }

func (e *agentError) Error() string { return e.msg }
