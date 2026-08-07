package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/openapi-mocker/internal/domain"
	"github.com/example/openapi-mocker/internal/usecase"
)

// customProvider allows connecting an arbitrary HTTP LLM API not covered by
// the built-in providers: the user specifies the URL, headers, request body
// template (with placeholders {{SYSTEM}}/{{USER}}/{{MODEL}}), and a dot-path
// for extracting text from the JSON response, e.g. "choices.0.message.content".
type customProvider struct {
	url      string
	model    string
	headers  map[string]string
	body     string
	respPath string
	client   *http.Client
}

func newCustom(cfg *domain.LLMProvider) provider {
	return &customProvider{
		url:      cfg.BaseURL,
		model:    cfg.Model,
		headers:  cfg.CustomHeaders,
		body:     cfg.CustomBody,
		respPath: cfg.CustomPath,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *customProvider) complete(ctx context.Context, req usecase.ChatRequest) (string, error) {
	bodyTemplate := p.body
	if bodyTemplate == "" {
		bodyTemplate = `{"model":"{{MODEL}}","messages":[{"role":"system","content":{{SYSTEM_JSON}}},` +
			`{"role":"user","content":{{USER_JSON}}}]}`
	}
	sysJSON, _ := json.Marshal(req.SystemPrompt)
	userJSON, _ := json.Marshal(req.UserPrompt)

	rendered := bodyTemplate
	rendered = strings.ReplaceAll(rendered, "{{MODEL}}", p.model)
	rendered = strings.ReplaceAll(rendered, "{{SYSTEM_JSON}}", string(sysJSON))
	rendered = strings.ReplaceAll(rendered, "{{USER_JSON}}", string(userJSON))
	rendered = strings.ReplaceAll(rendered, "{{SYSTEM}}", req.SystemPrompt)
	rendered = strings.ReplaceAll(rendered, "{{USER}}", req.UserPrompt)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader([]byte(rendered)))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("custom LLM request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("custom LLM returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if p.respPath == "" {
		return string(respBody), nil
	}
	var parsed interface{}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse custom LLM response: %w", err)
	}
	val, err := extractByPath(parsed, p.respPath)
	if err != nil {
		return "", err
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("value at path %q is not a string", p.respPath)
	}
	return s, nil
}

// extractByPath extracts a value from an arbitrary JSON structure by
// dot-path, e.g. "choices.0.message.content".
func extractByPath(v interface{}, path string) (interface{}, error) {
	parts := strings.Split(path, ".")
	cur := v
	for _, part := range parts {
		switch node := cur.(type) {
		case map[string]interface{}:
			next, ok := node[part]
			if !ok {
				return nil, fmt.Errorf("field %q not found in LLM response", part)
			}
			cur = next
		case []interface{}:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("invalid array index %q in LLM response", part)
			}
			cur = node[idx]
		default:
			return nil, fmt.Errorf("cannot descend into %q: unexpected node type", part)
		}
	}
	return cur, nil
}
