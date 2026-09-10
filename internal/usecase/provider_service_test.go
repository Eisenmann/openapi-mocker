package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestProviderService_List(t *testing.T) {
	t.Parallel()

	repo := newMockProviderRepo()
	svc := usecase.NewProviderService(repo, &mockLLMGateway{})

	repo.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai", ProjectID: "p1"})
	repo.CreateProvider(&domain.LLMProvider{Name: "global", Type: "anthropic"})

	providers := svc.List("p1")
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
}

func TestProviderService_Create(t *testing.T) {
	t.Parallel()

	t.Run("valid provider", func(t *testing.T) {
		t.Parallel()

		repo := newMockProviderRepo()
		svc := usecase.NewProviderService(repo, &mockLLMGateway{})

		p, err := svc.Create(&domain.LLMProvider{Name: "openai", Type: "openai"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if p.Name != "openai" {
			t.Errorf("expected name 'openai', got %q", p.Name)
		}
	})

	t.Run("missing name or type", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewProviderService(newMockProviderRepo(), &mockLLMGateway{})

		_, err := svc.Create(&domain.LLMProvider{Name: "", Type: "openai"})
		if !errors.Is(err, usecase.ErrNameAndTypeRequired) {
			t.Errorf("expected ErrNameAndTypeRequired, got %v", err)
		}

		_, err = svc.Create(&domain.LLMProvider{Name: "openai", Type: ""})
		if !errors.Is(err, usecase.ErrNameAndTypeRequired) {
			t.Errorf("expected ErrNameAndTypeRequired, got %v", err)
		}
	})
}

func TestProviderService_Update(t *testing.T) {
	t.Parallel()

	repo := newMockProviderRepo()
	svc := usecase.NewProviderService(repo, &mockLLMGateway{})

	created, _ := svc.Create(&domain.LLMProvider{Name: "openai", Type: "openai"})
	created.Model = "gpt-4"

	if err := svc.Update(created); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Not found.
	err := svc.Update(&domain.LLMProvider{ID: "missing"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProviderService_Delete(t *testing.T) {
	t.Parallel()

	repo := newMockProviderRepo()
	svc := usecase.NewProviderService(repo, &mockLLMGateway{})

	created, _ := svc.Create(&domain.LLMProvider{Name: "openai", Type: "openai"})

	err := svc.Delete(created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = svc.Delete(created.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProviderService_Test(t *testing.T) {
	t.Parallel()

	t.Run("provider not found", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewProviderService(newMockProviderRepo(), &mockLLMGateway{})

		err := svc.Test(context.Background(), "missing")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("successful test", func(t *testing.T) {
		t.Parallel()

		repo := newMockProviderRepo()
		repo.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		llm := &mockLLMGateway{response: "ok"}
		svc := usecase.NewProviderService(repo, llm)

		err := svc.Test(context.Background(), "provopenai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("LLM error", func(t *testing.T) {
		t.Parallel()

		repo := newMockProviderRepo()
		repo.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		llm := &mockLLMGateway{err: errors.New("llm down")}
		svc := usecase.NewProviderService(repo, llm)

		err := svc.Test(context.Background(), "provopenai")
		if err == nil {
			t.Fatal("expected error from LLM")
		}
	})
}
