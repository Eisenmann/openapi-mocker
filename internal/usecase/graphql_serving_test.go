package usecase_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestGraphQLServingService_Serve(t *testing.T) {
	t.Parallel()

	t.Run("no contract", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewGraphQLServingService(newMockContractRepo(), newMockLogRepo(), &mockGraphQLEngine{})

		_, err := svc.Serve("p1", "{ ping }", "", nil)
		if err == nil {
			t.Fatal("expected error for missing contract")
		}
	})

	t.Run("not graphql contract", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), &mockGraphQLEngine{})

		_, err := svc.Serve("p1", "{ ping }", "", nil)
		if err == nil {
			t.Fatal("expected error for non-graphql contract")
		}

		if !errors.Is(err, usecase.ErrNotGraphQLContract) {
			t.Errorf("expected ErrNotGraphQLContract, got %v", err)
		}
	})

	t.Run("successful execution", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{executeBody: []byte(`{"data":{"ping":"example"}}`)}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		body, err := svc.Serve("p1", "{ ping }", "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(body) != `{"data":{"ping":"example"}}` {
			t.Errorf("unexpected body: %q", body)
		}
	})

	t.Run("query error", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{
			executeErr: &usecase.GraphQLQueryError{
				Errors: []usecase.GraphQLQueryErrorItem{{Message: "unknown field"}},
			},
		}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		_, err := svc.Serve("p1", "{ unknown }", "", nil)
		if err == nil {
			t.Fatal("expected query error")
		}

		var qe *usecase.GraphQLQueryError
		if !errors.As(err, &qe) {
			t.Errorf("expected GraphQLQueryError, got %T", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{executeErr: errSentinel}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		_, err := svc.Serve("p1", "{ ping }", "", nil)
		if err == nil {
			t.Fatal("expected server error")
		}
	})
}

func TestGraphQLServingService_Schema(t *testing.T) {
	t.Parallel()

	t.Run("no contract", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewGraphQLServingService(newMockContractRepo(), newMockLogRepo(), &mockGraphQLEngine{})

		_, err := svc.Schema("p1")
		if err == nil {
			t.Fatal("expected error for missing contract")
		}
	})

	t.Run("not graphql", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), &mockGraphQLEngine{})

		_, err := svc.Schema("p1")
		if err == nil {
			t.Fatal("expected error for non-graphql contract")
		}
	})

	t.Run("successful introspection", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{introspection: "type Query { ping: String }"}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		sdl, err := svc.Schema("p1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if sdl != "type Query { ping: String }" {
			t.Errorf("unexpected SDL: %q", sdl)
		}
	})

	t.Run("introspection error", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{introspectionErr: errSentinel}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		_, err := svc.Schema("p1")
		if err == nil {
			t.Fatal("expected introspection error")
		}
	})
}

func TestGraphQLServingService_Operations(t *testing.T) {
	t.Parallel()

	t.Run("no contract", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewGraphQLServingService(newMockContractRepo(), newMockLogRepo(), &mockGraphQLEngine{})

		_, err := svc.Operations("p1")
		if err == nil {
			t.Fatal("expected error for missing contract")
		}
	})

	t.Run("successful operations", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{
			operations: []usecase.GraphQLOperation{
				{Type: "query", Name: "ping", ReturnType: "String!"},
			},
		}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		ops, err := svc.Operations("p1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(ops) != 1 {
			t.Fatalf("expected 1 operation, got %d", len(ops))
		}

		if ops[0].Name != "ping" {
			t.Errorf("expected operation 'ping', got %q", ops[0].Name)
		}
	})

	t.Run("operations error", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

		engine := &mockGraphQLEngine{operationsErr: errSentinel}
		svc := usecase.NewGraphQLServingService(repo, newMockLogRepo(), engine)

		_, err := svc.Operations("p1")
		if err == nil {
			t.Fatal("expected operations error")
		}
	})
}

func TestGraphQLServingService_Logs(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

	logs := newMockLogRepo()
	engine := &mockGraphQLEngine{executeBody: []byte(`{"data":{}}`)}
	svc := usecase.NewGraphQLServingService(repo, logs, engine)

	_, err := svc.Serve("p1", "{ ping }", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(logs.logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
	}

	log := logs.logs[0]
	if log.ProjectID != "p1" || log.Method != "POST" {
		t.Errorf("unexpected log: %+v", log)
	}

	if log.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", log.StatusCode)
	}

	if !log.Matched {
		t.Error("expected matched=true")
	}
}

func TestGraphQLServingService_Logs_Error(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

	logs := newMockLogRepo()
	engine := &mockGraphQLEngine{executeErr: errSentinel}
	svc := usecase.NewGraphQLServingService(repo, logs, engine)

	_, err := svc.Serve("p1", "{ ping }", "", nil)
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

func TestGraphQLServingService_Logs_QueryError(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "graphql", "type Query { ping: String }", "manual")

	logs := newMockLogRepo()
	engine := &mockGraphQLEngine{
		executeErr: &usecase.GraphQLQueryError{
			Errors: []usecase.GraphQLQueryErrorItem{{Message: "bad"}},
		},
	}
	svc := usecase.NewGraphQLServingService(repo, logs, engine)

	_, err := svc.Serve("p1", "{ bad }", "", nil)
	if err == nil {
		t.Fatal("expected query error")
	}

	if len(logs.logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs.logs))
	}

	if logs.logs[0].StatusCode != 200 {
		t.Errorf("expected status 200 for query error, got %d", logs.logs[0].StatusCode)
	}

	if logs.logs[0].Matched {
		t.Error("expected matched=false for query error")
	}
}

func TestGraphQLServingService_Logs_NoContract(t *testing.T) {
	t.Parallel()

	logs := newMockLogRepo()
	svc := usecase.NewGraphQLServingService(newMockContractRepo(), logs, &mockGraphQLEngine{})

	_, err := svc.Serve("p1", "{ ping }", "", nil)
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

func TestGraphQLQueryError_Error(t *testing.T) {
	t.Parallel()

	e := &usecase.GraphQLQueryError{
		Errors: []usecase.GraphQLQueryErrorItem{
			{Message: "first error"},
			{Message: "second error"},
		},
	}

	msg := e.Error()
	if msg != "first error; second error" {
		t.Errorf("unexpected error message: %q", msg)
	}
}

func TestStatusFromError(t *testing.T) {
	t.Parallel()

	if code := usecase.StatusFromError(domain.ErrNotFound); code != 404 {
		t.Errorf("expected 404 for ErrNotFound, got %d", code)
	}

	if code := usecase.StatusFromError(usecase.ErrNotGraphQLContract); code != 400 {
		t.Errorf("expected 400 for ErrNotGraphQLContract, got %d", code)
	}

	if code := usecase.StatusFromError(errSentinel); code != 500 {
		t.Errorf("expected 500 for generic error, got %d", code)
	}
}
