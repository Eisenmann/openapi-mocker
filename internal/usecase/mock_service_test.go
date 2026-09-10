package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestMockService_List(t *testing.T) {
	t.Parallel()

	repo := newMockMockRepo()
	svc := usecase.NewMockService(repo, newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	repo.CreateMock(&domain.MockRule{ProjectID: "p1", Path: "/users", Method: "GET"})
	repo.CreateMock(&domain.MockRule{ProjectID: "p2", Path: "/other", Method: "GET"})

	mocks := svc.List("p1")
	if len(mocks) != 1 {
		t.Fatalf("expected 1 mock, got %d", len(mocks))
	}
}

func TestMockService_Create(t *testing.T) {
	t.Parallel()

	t.Run("valid body", func(t *testing.T) {
		t.Parallel()

		repo := newMockMockRepo()
		svc := usecase.NewMockService(repo, newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		m, err := svc.Create(&domain.MockRule{
			ProjectID: "p1", Path: "/users", Method: "GET",
			Body: `{"name": "test"}`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if m.Scenario != "default" {
			t.Errorf("expected default scenario, got %q", m.Scenario)
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, err := svc.Create(&domain.MockRule{
			ProjectID: "p1", Path: "/users", Method: "GET",
			Body: "not json",
		})
		if err == nil {
			t.Fatal("expected error for invalid JSON body")
		}
	})

	t.Run("empty body is valid", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, err := svc.Create(&domain.MockRule{
			ProjectID: "p1", Path: "/users", Method: "GET",
			Body: "  ",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("custom scenario preserved", func(t *testing.T) {
		t.Parallel()

		repo := newMockMockRepo()
		svc := usecase.NewMockService(repo, newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		m, err := svc.Create(&domain.MockRule{
			ProjectID: "p1", Path: "/users", Method: "GET",
			Scenario: "error", Body: `{}`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if m.Scenario != "error" {
			t.Errorf("expected scenario 'error', got %q", m.Scenario)
		}
	})
}

func TestMockService_Update(t *testing.T) {
	t.Parallel()

	t.Run("valid update", func(t *testing.T) {
		t.Parallel()

		repo := newMockMockRepo()
		svc := usecase.NewMockService(repo, newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		created, _ := svc.Create(&domain.MockRule{
			ProjectID: "p1", Path: "/users", Method: "GET", Body: `{}`,
		})

		created.Body = `{"updated": true}`
		err := svc.Update(created)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		err := svc.Update(&domain.MockRule{ID: "x", Body: "bad"})
		if err == nil {
			t.Fatal("expected error for invalid body")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		err := svc.Update(&domain.MockRule{ID: "missing", Body: `{}`})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestMockService_Delete(t *testing.T) {
	t.Parallel()

	repo := newMockMockRepo()
	svc := usecase.NewMockService(repo, newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	created, _ := svc.Create(&domain.MockRule{
		ProjectID: "p1", Path: "/users", Method: "GET", Body: `{}`,
	})

	err := svc.Delete(created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = svc.Delete(created.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestMockService_GenerateBody(t *testing.T) {
	t.Parallel()

	t.Run("missing provider", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, _, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "", "")
		if !errors.Is(err, usecase.ErrProviderIDRequired) {
			t.Errorf("expected ErrProviderIDRequired, got %v", err)
		}
	})

	t.Run("no contract", func(t *testing.T) {
		t.Parallel()

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), providers, &mockContractEngine{}, &mockLLMGateway{})

		_, _, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "provopenai", "")
		if err == nil {
			t.Fatal("expected error for missing contract")
		}
	})

	t.Run("provider not found", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		svc := usecase.NewMockService(newMockMockRepo(), repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, _, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "missing", "")
		if err == nil {
			t.Fatal("expected error for missing provider")
		}
	})

	t.Run("schema error", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		engine := &mockContractEngine{schemaErr: errors.New("schema error")}
		svc := usecase.NewMockService(newMockMockRepo(), repo, providers, engine, &mockLLMGateway{})

		_, _, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "provopenai", "")
		if err == nil {
			t.Fatal("expected error for schema failure")
		}
	})

	t.Run("operation not found", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		engine := &mockContractEngine{found: false}
		svc := usecase.NewMockService(newMockMockRepo(), repo, providers, engine, &mockLLMGateway{})

		_, _, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "provopenai", "")
		if err == nil {
			t.Fatal("expected error for operation not found")
		}
	})

	t.Run("LLM error", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		engine := &mockContractEngine{found: true, pathTemplate: "/users", summary: "List"}
		llm := &mockLLMGateway{err: errors.New("llm down")}
		svc := usecase.NewMockService(newMockMockRepo(), repo, providers, engine, llm)

		_, _, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "provopenai", "")
		if err == nil {
			t.Fatal("expected error from LLM")
		}
	})

	t.Run("successful generation", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		engine := &mockContractEngine{
			found: true, pathTemplate: "/users", summary: "List",
			schemaJSON: `{"type": "object"}`,
		}
		llm := &mockLLMGateway{response: `{"name": "Ada"}`}
		svc := usecase.NewMockService(newMockMockRepo(), repo, providers, engine, llm)

		body, warning, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "provopenai", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if body != `{"name": "Ada"}` {
			t.Errorf("unexpected body: %q", body)
		}

		if warning != "" {
			t.Errorf("expected no warning, got %q", warning)
		}
	})

	t.Run("validation warning", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		repo.AddVersion("p1", "yaml", "raw", "manual")

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		engine := &mockContractEngine{
			found: true, pathTemplate: "/users", summary: "List",
			schemaJSON:      `{"type": "object"}`,
			validateBodyErr: errors.New("schema mismatch"),
		}
		llm := &mockLLMGateway{response: `{"name": "Ada"}`}
		svc := usecase.NewMockService(newMockMockRepo(), repo, providers, engine, llm)

		body, warning, err := svc.GenerateBody(context.Background(), "p1", "/users", "GET", 200, "provopenai", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if body != `{"name": "Ada"}` {
			t.Errorf("unexpected body: %q", body)
		}

		if warning == "" {
			t.Error("expected warning for schema mismatch")
		}
	})
}
