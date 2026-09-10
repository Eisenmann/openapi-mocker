package llm_test

import (
	"context"
	"strings"
	"testing"

	llm "github.com/Eisenmann/openapi-mocker/internal/adapter/llm"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestGateway_Complete(t *testing.T) {
	t.Parallel()

	g := llm.NewGateway()

	t.Run("unknown provider type", func(t *testing.T) {
		t.Parallel()

		_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: "unknown"}, usecase.ChatRequest{})
		if err == nil {
			t.Fatal("expected error for unknown provider type")
		}
	})

	t.Run("openai provider", func(t *testing.T) {
		t.Parallel()

		_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: "openai"}, usecase.ChatRequest{})
		if err == nil {
			t.Fatal("expected error (no server)")
		}
	})

	t.Run("anthropic provider", func(t *testing.T) {
		t.Parallel()

		_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: "anthropic"}, usecase.ChatRequest{})
		if err == nil {
			t.Fatal("expected error (no server)")
		}
	})

	t.Run("google provider", func(t *testing.T) {
		t.Parallel()

		_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: "google"}, usecase.ChatRequest{})
		if err == nil {
			t.Fatal("expected error (no server)")
		}
	})

	t.Run("custom provider", func(t *testing.T) {
		t.Parallel()

		_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: "custom"}, usecase.ChatRequest{})
		if err == nil {
			t.Fatal("expected error (no server)")
		}
	})
}

func TestGateway_Complete_AllOpenAICompatible(t *testing.T) {
	t.Parallel()

	g := llm.NewGateway()

	types := []string{"openai", "azure_openai", "ollama", "groq", "openrouter", "together", "vllm", "lmstudio", "deepseek"}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()

			_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: typ}, usecase.ChatRequest{})
			if err == nil {
				t.Fatal("expected error (no server)")
			}
		})
	}
}

func TestGateway_Complete_ErrorWrapping(t *testing.T) {
	t.Parallel()

	g := llm.NewGateway()

	// The error should mention the provider request failure.
	_, err := g.Complete(context.Background(), &domain.LLMProvider{Type: "openai"}, usecase.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "request to LLM provider failed") {
		t.Errorf("expected provider request failure message, got %v", err)
	}
}
