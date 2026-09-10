package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestContractService_GetActive(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	repo.AddVersion("p1", "yaml", "raw", "manual")

	c, err := svc.GetActive("p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.Version != 1 {
		t.Errorf("expected version 1, got %d", c.Version)
	}

	_, err = svc.GetActive("nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestContractService_GetVersion(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	repo.AddVersion("p1", "yaml", "v1", "manual")
	repo.AddVersion("p1", "yaml", "v2", "manual")

	c, err := svc.GetVersion("p1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.Raw != "v1" {
		t.Errorf("expected raw 'v1', got %q", c.Raw)
	}

	_, err = svc.GetVersion("p1", 99)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestContractService_ListVersions(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	repo.AddVersion("p1", "yaml", "v1", "manual")
	repo.AddVersion("p1", "yaml", "v2", "manual")

	versions := svc.ListVersions("p1")
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
}

func TestContractService_Validate(t *testing.T) {
	t.Parallel()

	engine := &mockContractEngine{
		validateResult: usecase.ValidationResult{Valid: true, PathCount: 3, OpCount: 5},
	}
	svc := usecase.NewContractService(newMockContractRepo(), newMockProviderRepo(), engine, &mockLLMGateway{})

	res := svc.Validate("raw")
	if !res.Valid {
		t.Error("expected valid result")
	}

	if res.PathCount != 3 || res.OpCount != 5 {
		t.Errorf("unexpected counts: %+v", res)
	}
}

func TestContractService_Publish(t *testing.T) {
	t.Parallel()

	t.Run("empty raw", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewContractService(newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, err := svc.Publish("p1", "   ", "manual")
		if err == nil {
			t.Fatal("expected error for empty contract")
		}
	})

	t.Run("invalid contract", func(t *testing.T) {
		t.Parallel()

		engine := &mockContractEngine{parseErr: errors.New("invalid")}
		svc := usecase.NewContractService(newMockContractRepo(), newMockProviderRepo(), engine, &mockLLMGateway{})

		_, err := svc.Publish("p1", "raw", "manual")
		if err == nil {
			t.Fatal("expected error for invalid contract")
		}
	})

	t.Run("yaml format", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		c, err := svc.Publish("p1", "openapi: 3.0.0", "upload")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if c.Format != "yaml" {
			t.Errorf("expected yaml format, got %q", c.Format)
		}

		if c.Source != "upload" {
			t.Errorf("expected source 'upload', got %q", c.Source)
		}
	})

	t.Run("json format", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		c, err := svc.Publish("p1", `{"openapi": "3.0.0"}`, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if c.Format != "json" {
			t.Errorf("expected json format, got %q", c.Format)
		}

		if c.Source != "manual" {
			t.Errorf("expected default source 'manual', got %q", c.Source)
		}
	})

	t.Run("graphql format", func(t *testing.T) {
		t.Parallel()

		repo := newMockContractRepo()
		svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		c, err := svc.Publish("p1", "type Query { ping: String }", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if c.Format != "graphql" {
			t.Errorf("expected graphql format, got %q", c.Format)
		}
	})
}

func TestContractService_Rollback(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	repo.AddVersion("p1", "yaml", "v1", "manual")
	repo.AddVersion("p1", "yaml", "v2", "manual")

	c, err := svc.Rollback("p1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.Raw != "v1" {
		t.Errorf("expected raw 'v1', got %q", c.Raw)
	}

	if c.Source != "rollback-to-v1" {
		t.Errorf("expected source 'rollback-to-v1', got %q", c.Source)
	}

	if c.Version != 3 {
		t.Errorf("expected version 3, got %d", c.Version)
	}

	_, err = svc.Rollback("p1", 99)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestContractService_Diff(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	engine := &mockContractEngine{
		diffLines: []usecase.DiffLine{
			{Type: usecase.DiffAdded, Text: "+ new"},
		},
	}
	svc := usecase.NewContractService(repo, newMockProviderRepo(), engine, &mockLLMGateway{})

	repo.AddVersion("p1", "yaml", "v1", "manual")
	repo.AddVersion("p1", "yaml", "v2", "manual")

	// Diff against active (toVersion == nil).
	diff, err := svc.Diff("p1", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if diff.FromVersion != 1 || diff.ToVersion != 2 {
		t.Errorf("unexpected versions: from=%d to=%d", diff.FromVersion, diff.ToVersion)
	}

	if len(diff.Lines) != 1 {
		t.Errorf("expected 1 diff line, got %d", len(diff.Lines))
	}

	// Diff against explicit version.
	diff, err = svc.Diff("p1", 1, intPtr(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if diff.ToVersion != 1 {
		t.Errorf("expected toVersion 1, got %d", diff.ToVersion)
	}

	// From version not found.
	_, err = svc.Diff("p1", 99, nil)
	if err == nil {
		t.Fatal("expected error for missing from version")
	}

	// To version not found.
	_, err = svc.Diff("p1", 1, intPtr(99))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestContractService_ListEndpoints(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	engine := &mockContractEngine{
		endpoints: []usecase.Endpoint{
			{Path: "/users", Method: "GET", Summary: "List users"},
		},
	}
	svc := usecase.NewContractService(repo, newMockProviderRepo(), engine, &mockLLMGateway{})

	repo.AddVersion("p1", "yaml", "raw", "manual")

	endpoints, err := svc.ListEndpoints("p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(endpoints))
	}

	if endpoints[0].Path != "/users" {
		t.Errorf("expected path /users, got %q", endpoints[0].Path)
	}

	// No contract.
	_, err = svc.ListEndpoints("nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestContractService_GenerateFromDescription(t *testing.T) {
	t.Parallel()

	t.Run("missing provider or description", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewContractService(newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, _, err := svc.GenerateFromDescription(context.Background(), "", "desc")
		if err == nil {
			t.Fatal("expected error for empty provider")
		}

		_, _, err = svc.GenerateFromDescription(context.Background(), "p1", "  ")
		if err == nil {
			t.Fatal("expected error for empty description")
		}
	})

	t.Run("provider not found", func(t *testing.T) {
		t.Parallel()

		svc := usecase.NewContractService(newMockContractRepo(), newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

		_, _, err := svc.GenerateFromDescription(context.Background(), "missing", "desc")
		if err == nil {
			t.Fatal("expected error for missing provider")
		}
	})

	t.Run("successful generation", func(t *testing.T) {
		t.Parallel()

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		llm := &mockLLMGateway{response: `{"openapi": "3.0.0"}`}
		svc := usecase.NewContractService(newMockContractRepo(), providers, &mockContractEngine{}, llm)

		raw, warning, err := svc.GenerateFromDescription(context.Background(), "provopenai", "Build a users API")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if raw != `{"openapi": "3.0.0"}` {
			t.Errorf("unexpected raw: %q", raw)
		}

		if warning != "" {
			t.Errorf("expected no warning, got %q", warning)
		}
	})

	t.Run("LLM error", func(t *testing.T) {
		t.Parallel()

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		llm := &mockLLMGateway{err: errors.New("llm down")}
		svc := usecase.NewContractService(newMockContractRepo(), providers, &mockContractEngine{}, llm)

		_, _, err := svc.GenerateFromDescription(context.Background(), "provopenai", "desc")
		if err == nil {
			t.Fatal("expected error from LLM")
		}
	})

	t.Run("invalid generated contract", func(t *testing.T) {
		t.Parallel()

		providers := newMockProviderRepo()
		providers.CreateProvider(&domain.LLMProvider{Name: "openai", Type: "openai"})

		llm := &mockLLMGateway{response: "not valid json"}
		engine := &mockContractEngine{parseErr: errors.New("invalid")}
		svc := usecase.NewContractService(newMockContractRepo(), providers, engine, llm)

		raw, warning, err := svc.GenerateFromDescription(context.Background(), "provopenai", "desc")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if raw != "not valid json" {
			t.Errorf("unexpected raw: %q", raw)
		}

		if warning == "" {
			t.Error("expected warning for invalid contract")
		}
	})
}

func intPtr(v int) *int {
	return &v
}
