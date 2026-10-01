package usecase_test

import (
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// validationFixture wires a MockServingService with a project in the given
// validation mode and an engine that reports the given violations.
func validationFixture(
	t *testing.T, mode domain.ValidationMode, violations []string,
) (*usecase.MockServingService, *mockContractEngine, *mockLogRepo) {
	t.Helper()

	projects := newMockProjectRepo()
	p := projects.Create("x", "")

	if _, err := projects.SetValidationMode(p.ID, mode); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	contracts := newMockContractRepo()
	contracts.AddVersion(p.ID, "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	mocks.CreateMock(&domain.MockRule{
		ProjectID: p.ID, Path: "/users", Method: "GET", StatusCode: 200,
		Headers: map[string]string{"X-Rule": "1"}, Body: `{"ok":true}`,
	})

	engine := &mockContractEngine{found: true, pathTemplate: "/users", requestViolations: violations}
	logs := newMockLogRepo()

	svc := usecase.NewMockServingService(contracts, mocks, logs, engine, usecase.WithProjects(projects))

	return svc, engine, logs
}

func serveUsers(svc *usecase.MockServingService) usecase.MockResponse {
	return svc.ServeRequest("px", &usecase.MockRequest{
		Method: "GET", Path: "/users", Query: map[string][]string{"limit": {"x"}},
	})
}

func TestServeRequest_ValidationOff(t *testing.T) {
	t.Parallel()

	for _, mode := range []domain.ValidationMode{"", domain.ValidationOff} {
		svc, engine, logs := validationFixture(t, mode, []string{"bad"})

		resp := serveUsers(svc)
		if resp.StatusCode != 200 || engine.validateRequestCalls != 0 {
			t.Errorf("mode %q: request must be served unvalidated, got %d (validations=%d)",
				mode, resp.StatusCode, engine.validateRequestCalls)
		}

		if len(logs.logs[0].Violations) != 0 {
			t.Errorf("mode %q: no violations may be logged", mode)
		}
	}
}

func TestServeRequest_Warn(t *testing.T) {
	t.Parallel()

	svc, _, logs := validationFixture(t, domain.ValidationWarn, []string{"query parameter \"limit\": bad"})

	resp := serveUsers(svc)
	if resp.StatusCode != 200 || resp.Source != "rule" {
		t.Fatalf("warn mode must serve the mock, got %d from %q", resp.StatusCode, resp.Source)
	}

	if got := resp.Headers["X-Mock-Validation"]; !strings.Contains(got, "1 violation") {
		t.Errorf("warn header missing, got %q", got)
	}

	if resp.Headers["X-Rule"] != "1" {
		t.Error("the rule's own headers must be kept")
	}

	if v := logs.logs[0].Violations; len(v) != 1 || !strings.Contains(v[0], "limit") {
		t.Errorf("violations must be logged, got %v", v)
	}
}

func TestServeRequest_WarnDoesNotMutateRuleHeaders(t *testing.T) {
	t.Parallel()

	svc, _, _ := validationFixture(t, domain.ValidationWarn, []string{"bad"})
	serveUsers(svc)

	// A second request without violations must not inherit the warning header.
	svc2, _, _ := validationFixture(t, domain.ValidationWarn, nil)

	if _, ok := serveUsers(svc2).Headers["X-Mock-Validation"]; ok {
		t.Error("valid request must not carry the warning header")
	}
}

func TestServeRequest_Enforce(t *testing.T) {
	t.Parallel()

	svc, _, logs := validationFixture(t, domain.ValidationEnforce, []string{"query parameter \"limit\": bad"})

	resp := serveUsers(svc)
	if resp.StatusCode != 400 || resp.Source != "request-validation" {
		t.Fatalf("want 400 from request-validation, got %d from %q", resp.StatusCode, resp.Source)
	}

	body := string(resp.Body)
	if !strings.Contains(body, "violations") || !strings.Contains(body, "limit") {
		t.Errorf("body must list the violations, got %s", body)
	}

	l := logs.logs[0]
	if l.StatusCode != 400 || l.Matched || len(l.Violations) != 1 {
		t.Errorf("rejected request must be logged as unmatched with violations: %+v", l)
	}
}

func TestServeRequest_EnforceValidRequestIsServed(t *testing.T) {
	t.Parallel()

	svc, engine, _ := validationFixture(t, domain.ValidationEnforce, nil)

	resp := serveUsers(svc)
	if resp.StatusCode != 200 || engine.validateRequestCalls != 1 {
		t.Errorf("valid request must be served after validation, got %d (validations=%d)",
			resp.StatusCode, engine.validateRequestCalls)
	}
}

func TestServeRequest_UnknownOperationIsNotValidated(t *testing.T) {
	t.Parallel()

	svc, engine, _ := validationFixture(t, domain.ValidationEnforce, []string{"bad"})
	engine.found = false

	resp := svc.ServeRequest("px", &usecase.MockRequest{Method: "GET", Path: "/nope"})
	if resp.StatusCode != 404 || engine.validateRequestCalls != 0 {
		t.Errorf("unknown endpoint must stay a 404, got %d (validations=%d)", resp.StatusCode, engine.validateRequestCalls)
	}
}

func TestServe_NeverValidates(t *testing.T) {
	t.Parallel()

	svc, engine, _ := validationFixture(t, domain.ValidationEnforce, []string{"bad"})

	resp := svc.Serve("px", "GET", "/users", "")
	if resp.StatusCode != 200 || engine.validateRequestCalls != 0 {
		t.Errorf("Serve is the unvalidated entry point, got %d (validations=%d)", resp.StatusCode, engine.validateRequestCalls)
	}
}

func TestServeRequest_WithoutProjectsNeverValidates(t *testing.T) {
	t.Parallel()

	contracts := newMockContractRepo()
	contracts.AddVersion("p1", "yaml", "raw", "manual")

	engine := &mockContractEngine{
		found: true, pathTemplate: "/users", requestViolations: []string{"bad"},
		exampleBody: []byte(`{}`), exampleCT: "application/json",
	}
	svc := usecase.NewMockServingService(contracts, newMockMockRepo(), newMockLogRepo(), engine)

	resp := svc.ServeRequest("p1", &usecase.MockRequest{Method: "GET", Path: "/users"})
	if resp.StatusCode != 200 || engine.validateRequestCalls != 0 {
		t.Errorf("without WithProjects nothing may be validated, got %d", resp.StatusCode)
	}
}

// ---------- MCP ----------.

func mcpFixture(t *testing.T, mode domain.ValidationMode) (*usecase.MCPServingService, *mockMCPEngine, *mockLogRepo) {
	t.Helper()

	projects := newMockProjectRepo()
	p := projects.Create("x", "")

	if _, err := projects.SetValidationMode(p.ID, mode); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	contracts := newMockContractRepo()
	contracts.AddVersion(p.ID, usecase.FormatMCP, mcpManifest, "manual")

	engine := &mockMCPEngine{executeBody: []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), violations: []string{"bad arg"}}
	logs := newMockLogRepo()

	return usecase.NewMCPServingService(contracts, logs, engine, usecase.WithProjects(projects)), engine, logs
}

func TestMCPServing_ValidationOffUsesPlainExecute(t *testing.T) {
	t.Parallel()

	svc, engine, logs := mcpFixture(t, domain.ValidationOff)

	if _, err := svc.Serve("px", []byte(`{}`), ""); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	if engine.executeCalls != 1 || engine.validatedCalls != 0 {
		t.Errorf("off mode must use Execute (execute=%d validated=%d)", engine.executeCalls, engine.validatedCalls)
	}

	if len(logs.logs[0].Violations) != 0 {
		t.Error("no violations may be logged when off")
	}
}

func TestMCPServing_WarnAndEnforce(t *testing.T) {
	t.Parallel()

	cases := map[domain.ValidationMode]bool{domain.ValidationWarn: false, domain.ValidationEnforce: true}

	for mode, wantEnforce := range cases {
		svc, engine, logs := mcpFixture(t, mode)

		if _, err := svc.Serve("px", []byte(`{}`), ""); err != nil {
			t.Fatalf("Serve: %v", err)
		}

		if engine.validatedCalls != 1 || engine.lastEnforce != wantEnforce {
			t.Errorf("mode %s: validated=%d enforce=%v, want enforce=%v",
				mode, engine.validatedCalls, engine.lastEnforce, wantEnforce)
		}

		if v := logs.logs[0].Violations; len(v) != 1 || v[0] != "bad arg" {
			t.Errorf("mode %s: violations must be logged, got %v", mode, v)
		}
	}
}

func TestMCPServing_HandleReportsOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode         domain.ValidationMode
		wantRejected bool
	}{
		{domain.ValidationWarn, false},
		{domain.ValidationEnforce, true},
	}

	for _, tc := range cases {
		svc, _, _ := mcpFixture(t, tc.mode)

		got, err := svc.Handle("px", []byte(`{}`), "")
		if err != nil {
			t.Fatalf("Handle: %v", err)
		}

		if len(got.Violations) != 1 || got.Rejected != tc.wantRejected {
			t.Errorf("mode %s: got %+v, want rejected=%v", tc.mode, got, tc.wantRejected)
		}
	}

	svc, _, _ := mcpFixture(t, domain.ValidationOff)

	got, err := svc.Handle("px", []byte(`{}`), "")
	if err != nil || len(got.Violations) != 0 || got.Rejected {
		t.Errorf("off mode: want no violations, got %+v, %v", got, err)
	}
}

func TestMCPServing_WithoutProjectsNeverValidates(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

	engine := &mockMCPEngine{executeBody: []byte(`{}`)}
	svc := usecase.NewMCPServingService(repo, newMockLogRepo(), engine)

	if _, err := svc.Serve("p1", []byte(`{}`), ""); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	if engine.validatedCalls != 0 {
		t.Error("without WithProjects nothing may be validated")
	}
}

// ---------- Project service ----------.

func TestProjectService_SetValidationMode(t *testing.T) {
	t.Parallel()

	repo := newMockProjectRepo()
	svc := usecase.NewProjectService(repo)
	p, _ := svc.Create("x", "")

	for _, mode := range []domain.ValidationMode{domain.ValidationWarn, domain.ValidationEnforce, domain.ValidationOff} {
		got, err := svc.SetValidationMode(p.ID, mode)
		if err != nil || got.ValidationMode != mode {
			t.Errorf("mode %q: got %+v, %v", mode, got, err)
		}
	}

	got, err := svc.SetValidationMode(p.ID, "")
	if err != nil || got.ValidationMode != domain.ValidationOff {
		t.Errorf("empty mode must mean off, got %+v, %v", got, err)
	}

	if _, err := svc.SetValidationMode(p.ID, "strict"); err == nil {
		t.Error("unknown mode must be rejected")
	}

	if _, err := svc.SetValidationMode("missing", domain.ValidationWarn); err == nil {
		t.Error("unknown project must be rejected")
	}
}
