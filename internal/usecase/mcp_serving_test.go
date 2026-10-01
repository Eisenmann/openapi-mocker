package usecase_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const mcpManifest = `{"mcpServer": {"name": "demo", "tools": [{"name": "ping"}]}}`

func TestMCPServingService_Serve(t *testing.T) {
	t.Parallel()

	t.Run("no contract", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMCPServingService(newMockContractRepo(), newMockLogRepo(), &mockMCPEngine{})

		_, err := svc.Serve("p1", []byte("{}"), "")
		if err == nil {
			t.Fatal("expected error for missing contract")
		}
	})

	t.Run("not mcp contract", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		svc := usecase.NewMCPServingService(repo, newMockLogRepo(), &mockMCPEngine{})

		_, err := svc.Serve("p1", []byte("{}"), "")
		if err == nil {
			t.Fatal("expected error for non-mcp contract")
		}

		if !errors.Is(err, usecase.ErrNotMCPContract) {
			t.Errorf("expected ErrNotMCPContract, got %v", err)
		}
	})

	t.Run("successful execution", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

		engine := &mockMCPEngine{executeBody: []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)}
		svc := usecase.NewMCPServingService(repo, newMockLogRepo(), engine)

		body, err := svc.Serve("p1", []byte("{}"), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(body) != `{"jsonrpc":"2.0","id":1,"result":{}}` {
			t.Errorf("unexpected body: %q", body)
		}
	})

	t.Run("notification returns nil body", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

		engine := &mockMCPEngine{executeBody: nil}
		svc := usecase.NewMCPServingService(repo, newMockLogRepo(), engine)

		body, err := svc.Serve("p1", []byte("{}"), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if body != nil {
			t.Errorf("expected nil body for notification, got %q", body)
		}
	})

	t.Run("engine load failure", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

		engine := &mockMCPEngine{executeErr: errSentinel}
		svc := usecase.NewMCPServingService(repo, newMockLogRepo(), engine)

		_, err := svc.Serve("p1", []byte("{}"), "")
		if err == nil {
			t.Fatal("expected server error")
		}
	})
}

func TestMCPServingService_Tools(t *testing.T) {
	t.Parallel()

	t.Run("no contract", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMCPServingService(newMockContractRepo(), newMockLogRepo(), &mockMCPEngine{})

		_, err := svc.Tools("p1")
		if err == nil {
			t.Fatal("expected error for missing contract")
		}
	})

	t.Run("successful tools", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

		engine := &mockMCPEngine{listToolsResult: []usecase.MCPTool{{Name: "ping"}}}
		svc := usecase.NewMCPServingService(repo, newMockLogRepo(), engine)

		tools, err := svc.Tools("p1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(tools) != 1 || tools[0].Name != "ping" {
			t.Errorf("unexpected tools: %+v", tools)
		}
	})
}

func TestMCPServingService_Logs(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

	logs := newMockLogRepo()
	engine := &mockMCPEngine{executeBody: []byte(`{}`)}
	svc := usecase.NewMCPServingService(repo, logs, engine)

	_, err := svc.Serve("p1", []byte("{}"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(logs.logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
	}

	logEntry := logs.logs[0]
	if logEntry.ProjectID != "p1" || logEntry.Method != "POST" {
		t.Errorf("unexpected log: %+v", logEntry)
	}

	if logEntry.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", logEntry.StatusCode)
	}

	if !logEntry.Matched {
		t.Error("expected matched=true")
	}

	wantPath := "/mock/p1/mcp"
	if logEntry.Path != wantPath {
		t.Errorf("expected path %q, got %q", wantPath, logEntry.Path)
	}
}

func TestMCPServingService_Logs_Error(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

	logs := newMockLogRepo()
	engine := &mockMCPEngine{executeErr: errSentinel}
	svc := usecase.NewMCPServingService(repo, logs, engine)

	_, err := svc.Serve("p1", []byte("{}"), "")
	if err == nil {
		t.Fatal("expected error")
	}

	if len(logs.logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
	}

	if logs.logs[0].StatusCode != 500 {
		t.Errorf("expected status 500, got %d", logs.logs[0].StatusCode)
	}
}

func TestMCPServingService_Logs_NoContract(t *testing.T) {
	t.Parallel()

	logs := newMockLogRepo()
	svc := usecase.NewMCPServingService(newMockContractRepo(), logs, &mockMCPEngine{})

	_, err := svc.Serve("p1", []byte("{}"), "")
	if err == nil {
		t.Fatal("expected error")
	}

	if len(logs.logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
	}

	if logs.logs[0].StatusCode != 404 {
		t.Errorf("expected status 404, got %d", logs.logs[0].StatusCode)
	}
}

func TestMCPServingService_Logs_Details(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		body        string
		scenario    string
		resp        []byte
		wantRule    string
		wantStatus  int
		wantMatched bool
	}{
		{"tool call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_forecast"}}`, "rainy", []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), "tools/call get_forecast [rainy]", 200, true},
		{"jsonrpc error", `{"jsonrpc":"2.0","id":1,"method":"nope"}`, "", []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"x"}}`), "nope", 200, false},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "", nil, "notifications/initialized", 202, true},
		{"batch with error", `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"x"}]`, "", []byte(`[{"id":1,"result":{}},{"id":2,"error":{"code":-32601}}]`), "batch(2)", 200, false},
		{"garbage", `{bad`, "", []byte(`{"error":{"code":-32700}}`), "", 200, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockContractRepo()
			repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

			logs := newMockLogRepo()
			svc := usecase.NewMCPServingService(repo, logs, &mockMCPEngine{executeBody: tc.resp})

			if _, err := svc.Serve("p1", []byte(tc.body), tc.scenario); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(logs.logs) != 1 {
				t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
			}

			l := logs.logs[0]
			if l.MatchedRule != tc.wantRule || l.StatusCode != tc.wantStatus || l.Matched != tc.wantMatched {
				t.Errorf("got rule=%q status=%d matched=%v; want rule=%q status=%d matched=%v",
					l.MatchedRule, l.StatusCode, l.Matched, tc.wantRule, tc.wantStatus, tc.wantMatched)
			}
		})
	}
}
