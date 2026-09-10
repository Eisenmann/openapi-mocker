package usecase_test

import (
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestMockServingService_Serve_NoContract(t *testing.T) {
	t.Parallel()

	logs := newMockLogRepo()
	svc := usecase.NewMockServingService(newMockContractRepo(), newMockMockRepo(), logs, &mockContractEngine{})

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	if !strings.Contains(string(resp.Body), "no contract") {
		t.Errorf("expected error body, got %q", resp.Body)
	}

	if len(logs.logs) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(logs.logs))
	}
}

func TestMockServingService_Serve_OperationNotFound(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	engine := &mockContractEngine{found: false}
	svc := usecase.NewMockServingService(repo, newMockMockRepo(), newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/missing", "")
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	if !strings.Contains(string(resp.Body), "endpoint not described") {
		t.Errorf("expected endpoint error body, got %q", resp.Body)
	}
}

func TestMockServingService_Serve_FromRule(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	mocks.CreateMock(&domain.MockRule{
		ProjectID: "p1", Path: "/users", Method: "GET",
		StatusCode: 201, ContentType: "application/json",
		Body: `{"created": true}`, DelayMs: 10,
	})

	engine := &mockContractEngine{found: true, pathTemplate: "/users"}
	svc := usecase.NewMockServingService(repo, mocks, newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.StatusCode != 201 {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	if string(resp.Body) != `{"created": true}` {
		t.Errorf("unexpected body: %q", resp.Body)
	}

	if resp.Source != "rule" {
		t.Errorf("expected source 'rule', got %q", resp.Source)
	}

	if resp.Matched != true {
		t.Error("expected matched=true")
	}

	if resp.DelayMs != 10 {
		t.Errorf("expected delay 10, got %d", resp.DelayMs)
	}
}

func TestMockServingService_Serve_FromRule_DefaultStatusCode(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	mocks.CreateMock(&domain.MockRule{
		ProjectID: "p1", Path: "/users", Method: "GET",
		Body: `{}`,
	})

	engine := &mockContractEngine{found: true, pathTemplate: "/users"}
	svc := usecase.NewMockServingService(repo, mocks, newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.StatusCode != 200 {
		t.Errorf("expected default 200, got %d", resp.StatusCode)
	}
}

func TestMockServingService_Serve_ScenarioMatching(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	mocks.CreateMock(&domain.MockRule{
		ProjectID: "p1", Path: "/users", Method: "GET",
		Scenario: "error", StatusCode: 500, Body: `{"error": "boom"}`,
	})
	mocks.CreateMock(&domain.MockRule{
		ProjectID: "p1", Path: "/users", Method: "GET",
		Scenario: "default", StatusCode: 200, Body: `{"ok": true}`,
	})

	engine := &mockContractEngine{found: true, pathTemplate: "/users"}
	svc := usecase.NewMockServingService(repo, mocks, newMockLogRepo(), engine)

	// Scenario match.
	resp := svc.Serve("p1", "GET", "/users", "error")
	if resp.StatusCode != 500 {
		t.Errorf("expected 500 for error scenario, got %d", resp.StatusCode)
	}

	// Fallback to default scenario.
	resp = svc.Serve("p1", "GET", "/users", "unknown")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for fallback, got %d", resp.StatusCode)
	}
}

func TestMockServingService_Serve_ChaosInjection(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	mocks.CreateMock(&domain.MockRule{
		ProjectID: "p1", Path: "/users", Method: "GET",
		StatusCode: 200, Body: `{}`, FailRatePct: 100,
	})

	engine := &mockContractEngine{found: true, pathTemplate: "/users"}
	svc := usecase.NewMockServingService(repo, mocks, newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.StatusCode < 500 || resp.StatusCode > 503 {
		t.Errorf("expected 5xx chaos status, got %d", resp.StatusCode)
	}

	if resp.Source != "chaos-injection" {
		t.Errorf("expected source 'chaos-injection', got %q", resp.Source)
	}
}

func TestMockServingService_Serve_SchemaExample(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	engine := &mockContractEngine{
		found: true, pathTemplate: "/users",
		exampleBody: []byte(`{"name": "Ada"}`), exampleCT: "application/json",
	}
	svc := usecase.NewMockServingService(repo, newMockMockRepo(), newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	if string(resp.Body) != `{"name": "Ada"}` {
		t.Errorf("unexpected body: %q", resp.Body)
	}

	if resp.Source != "schema-example" {
		t.Errorf("expected source 'schema-example', got %q", resp.Source)
	}
}

func TestMockServingService_Serve_SchemaExampleError(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	engine := &mockContractEngine{
		found: true, pathTemplate: "/users",
		exampleErr: errSentinel,
	}
	svc := usecase.NewMockServingService(repo, newMockMockRepo(), newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.StatusCode != 500 {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}

	if !strings.Contains(string(resp.Body), "failed to build response example") {
		t.Errorf("expected error body, got %q", resp.Body)
	}
}

func TestMockServingService_Serve_DefaultContentType(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	engine := &mockContractEngine{
		found: true, pathTemplate: "/users",
		exampleBody: []byte(`{}`), exampleCT: "",
	}
	svc := usecase.NewMockServingService(repo, newMockMockRepo(), newMockLogRepo(), engine)

	resp := svc.Serve("p1", "GET", "/users", "")
	if resp.ContentType != "application/json" {
		t.Errorf("expected default content type, got %q", resp.ContentType)
	}
}

func TestMockServingService_Serve_LogsRequest(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	logs := newMockLogRepo()
	engine := &mockContractEngine{
		found: true, pathTemplate: "/users",
		exampleBody: []byte(`{}`), exampleCT: "application/json",
	}
	svc := usecase.NewMockServingService(repo, newMockMockRepo(), logs, engine)

	svc.Serve("p1", "GET", "/users", "")

	if len(logs.logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
	}

	log := logs.logs[0]
	if log.ProjectID != "p1" || log.Method != "GET" || log.Path != "/users" {
		t.Errorf("unexpected log: %+v", log)
	}

	if log.StatusCode != 200 {
		t.Errorf("expected status 200 in log, got %d", log.StatusCode)
	}

	if !log.Matched {
		t.Error("expected matched=true in log")
	}
}
