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

type anthropicProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newAnthropic(cfg *domain.LLMProvider) *anthropicProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}

	model := cfg.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	return &anthropicProvider{
		baseURL: base,
		apiKey:  cfg.APIKey,
		model:   model,
		client:  &http.Client{Timeout: defaultLLMTimeout},
	}
}

func (p *anthropicProvider) complete(ctx context.Context, req usecase.ChatRequest) (string, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := map[string]interface{}{
		"model":      p.model,
		"max_tokens": maxTokens,
		"system":     req.SystemPrompt,
		"messages": []map[string]string{
			{roleField: userRole, contentField: req.UserPrompt},
		},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic %w: %w", errProviderRequestFailed, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= maxSuccessHTTPStatus {
		return "", fmt.Errorf("anthropic returned status %d: %s: %w",
			resp.StatusCode, string(respBody), errProviderRequestFailed)
	}

	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	err = json.Unmarshal(respBody, &parsed)
	if err != nil {
		return "", fmt.Errorf("failed to parse Anthropic response: %w", err)
	}

	if len(parsed.Content) == 0 {
		return "", fmt.Errorf("anthropic %w", errProviderEmptyResponse)
	}

	return parsed.Content[0].Text, nil
}
