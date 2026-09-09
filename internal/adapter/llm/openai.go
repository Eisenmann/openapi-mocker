package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type openAICompatible struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// newOpenAICompatible works with any backend implementing
// POST {baseURL}/chat/completions (OpenAI, Azure OpenAI, Ollama >=0.1.35,
// LM Studio, vLLM with --api-key, Groq, OpenRouter, Together, DeepSeek, etc.)
func newOpenAICompatible(cfg *domain.LLMProvider) *openAICompatible {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}

	return &openAICompatible{
		baseURL: base,
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		client:  &http.Client{Timeout: defaultLLMTimeout},
	}
}

func (p *openAICompatible) complete(ctx context.Context, req usecase.ChatRequest) (string, error) {
	body := map[string]interface{}{
		"model": p.model,
		"messages": []map[string]string{
			{roleField: systemRole, contentField: req.SystemPrompt},
			{roleField: userRole, contentField: req.UserPrompt},
		},
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal LLM request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")

	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LLM %w: %w", errProviderRequestFailed, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= maxSuccessHTTPStatus {
		return "", fmt.Errorf("LLM returned status %d: %s: %w", resp.StatusCode, string(respBody), errProviderRequestFailed)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	err = json.Unmarshal(respBody, &parsed)
	if err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("LLM %w", errProviderEmptyResponse)
	}

	return parsed.Choices[0].Message.Content, nil
}
