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

type googleProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newGoogle(cfg *domain.LLMProvider) *googleProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}

	model := cfg.Model
	if model == "" {
		model = "gemini-2.0-flash"
	}

	return &googleProvider{
		baseURL: base,
		apiKey:  cfg.APIKey,
		model:   model,
		client:  &http.Client{Timeout: defaultLLMTimeout},
	}
}

func (p *googleProvider) complete(ctx context.Context, req usecase.ChatRequest) (string, error) {
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{roleField: userRole, partsField: []map[string]string{{"text": req.UserPrompt}}},
		},
		"systemInstruction": map[string]interface{}{
			partsField: []map[string]string{{"text": req.SystemPrompt}},
		},
		"generationConfig": map[string]interface{}{
			"temperature": req.Temperature,
		},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, p.model, p.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("google Gemini %w: %w", errProviderRequestFailed, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= maxSuccessHTTPStatus {
		return "", fmt.Errorf("gemini returned status %d: %s: %w",
			resp.StatusCode, string(respBody), errProviderRequestFailed)
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	err = json.Unmarshal(respBody, &parsed)
	if err != nil {
		return "", fmt.Errorf("failed to parse Gemini response: %w", err)
	}

	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini %w", errProviderEmptyResponse)
	}

	return parsed.Candidates[0].Content.Parts[0].Text, nil
}
