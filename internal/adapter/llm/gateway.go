// Package llm is the adapter implementing usecase.LLMGateway on top of the
// HTTP APIs of various LLM providers. The usecase layer only knows about
// usecase.LLMGateway.Complete(ctx, *domain.LLMProvider, usecase.ChatRequest);
// the fact that five different HTTP protocols lie behind it is an
// implementation detail of this package.
package llm

import (
	"context"
	"fmt"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// provider is an internal interface for a specific HTTP backend. It is not
// exported: from outside the package only Gateway is visible.
type provider interface {
	complete(ctx context.Context, req usecase.ChatRequest) (string, error)
}

type Gateway struct{}

func NewGateway() *Gateway { return &Gateway{} }

var _ usecase.LLMGateway = (*Gateway)(nil)

func (g *Gateway) Complete(ctx context.Context, cfg *domain.LLMProvider, req usecase.ChatRequest) (string, error) {
	p, err := build(cfg)
	if err != nil {
		return "", err
	}
	return p.complete(ctx, req)
}

func build(cfg *domain.LLMProvider) (provider, error) {
	switch cfg.Type {
	case "openai", "azure_openai", "ollama", "groq", "openrouter", "together", "vllm", "lmstudio", "deepseek":
		// All listed providers (including local ones — Ollama/vLLM/LM Studio)
		// are compatible with the OpenAI Chat Completions API; only BaseURL
		// and model differ.
		return newOpenAICompatible(cfg), nil
	case "anthropic":
		return newAnthropic(cfg), nil
	case "google":
		return newGoogle(cfg), nil
	case "custom":
		return newCustom(cfg), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider type: %s", cfg.Type)
	}
}
