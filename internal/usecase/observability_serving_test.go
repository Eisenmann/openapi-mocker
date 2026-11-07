package usecase_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestGraphQLServing_LogsCapturedBodies(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())
	engine := &mockGraphQLEngine{executeBody: []byte(`{"data":{"ping":"example"}}`)}
	svc := usecase.NewGraphQLServingService(repo, hub, engine)

	_, err := svc.Serve("p1", "{ ping }", "Ping", map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}

	l, ok := hub.Get("p1", hub.ListLogs("p1", 1)[0].ID)
	if !ok || l.Endpoint != "graphql" || !l.Captured {
		t.Fatalf("entry = %+v", l)
	}

	if l.Request.Body != `{"query":"{ ping }","operationName":"Ping","variables":{"a":1}}` ||
		l.Response.Body != `{"data":{"ping":"example"}}` {
		t.Errorf("request=%q response=%q", l.Request.Body, l.Response.Body)
	}
}

func TestMCPServing_LogsCapturedBodiesAndEndpoint(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", usecase.FormatMCP, mcpManifest, "manual")

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())
	engine := &mockMCPEngine{executeBody: []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)}
	svc := usecase.NewMCPServingService(repo, hub, engine)

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping"}}`

	_, err := svc.Serve("p1", []byte(req), "rainy")
	if err != nil {
		t.Fatal(err)
	}

	l, _ := hub.Get("p1", hub.ListLogs("p1", 1)[0].ID)
	if l.Request.Body != req || l.Response.Body != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Errorf("bodies: %q / %q", l.Request.Body, l.Response.Body)
	}

	// The scenario is chosen by the client and is not part of the endpoint.
	if l.Endpoint != "tools/call ping" {
		t.Errorf("endpoint = %q", l.Endpoint)
	}
}

func TestRawCaptureIsBounded(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.CapturePolicy{Enabled: true, MaxBytes: usecase.MaxCaptureBytes})
	repo := newMockContractRepo()
	repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

	engine := &mockGraphQLEngine{executeBody: make([]byte, 2<<20)}
	svc := usecase.NewGraphQLServingService(repo, hub, engine)

	_, _ = svc.Serve("p1", "{ ping }", "", nil)

	l, _ := hub.Get("p1", hub.ListLogs("p1", 1)[0].ID)
	if len(l.Response.Body) > usecase.MaxCaptureBytes || !l.Response.Truncated || l.Response.Size != 2<<20 {
		t.Errorf("response = size %d, kept %d, truncated %v", l.Response.Size, len(l.Response.Body), l.Response.Truncated)
	}
}
