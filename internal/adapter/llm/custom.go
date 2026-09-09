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

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
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

func newCustom(cfg *domain.LLMProvider) *customProvider {
	return &customProvider{
		url:      cfg.BaseURL,
		model:    cfg.Model,
		headers:  cfg.CustomHeaders,
		body:     cfg.CustomBody,
		respPath: cfg.CustomPath,
		client:   &http.Client{Timeout: defaultLLMTimeout},
	}
}

func (p *customProvider) complete(ctx context.Context, req usecase.ChatRequest) (string, error) {
	rendered, err := renderBodyTemplate(p.body, p.model, req)
	if err != nil {
		return "", err
	}

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
		return "", fmt.Errorf("custom LLM %w: %w", errProviderRequestFailed, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= maxSuccessHTTPStatus {
		return "", fmt.Errorf("custom LLM returned status %d: %s: %w",
			resp.StatusCode, string(respBody), errProviderRequestFailed)
	}

	if p.respPath == "" {
		return string(respBody), nil
	}

	return extractCustomResponse(respBody, p.respPath)
}

// renderBodyTemplate fills the custom body template with the request data.
// An empty template falls back to an OpenAI-compatible chat-completions body.
func renderBodyTemplate(tpl, model string, req usecase.ChatRequest) (string, error) {
	if tpl == "" {
		tpl = `{"model":"{{MODEL}}","messages":[{"role":"system","content":{{SYSTEM_JSON}}},` +
			`{"role":"user","content":{{USER_JSON}}}]}`
	}

	sysJSON, err := json.Marshal(req.SystemPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to marshal system prompt: %w", err)
	}

	userJSON, err := json.Marshal(req.UserPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to marshal user prompt: %w", err)
	}

	rendered := tpl
	rendered = strings.ReplaceAll(rendered, "{{MODEL}}", model)
	rendered = strings.ReplaceAll(rendered, "{{SYSTEM_JSON}}", string(sysJSON))
	rendered = strings.ReplaceAll(rendered, "{{USER_JSON}}", string(userJSON))
	rendered = strings.ReplaceAll(rendered, "{{SYSTEM}}", req.SystemPrompt)
	rendered = strings.ReplaceAll(rendered, "{{USER}}", req.UserPrompt)

	return rendered, nil
}

// extractCustomResponse parses the raw response body and pulls the string
// value at the configured dot-path.
func extractCustomResponse(body []byte, respPath string) (string, error) {
	var parsed interface{}

	err := json.Unmarshal(body, &parsed)
	if err != nil {
		return "", fmt.Errorf("failed to parse custom LLM response: %w", err)
	}

	val, err := extractByPath(parsed, respPath)
	if err != nil {
		return "", err
	}

	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("value at path %q is not a string: %w", respPath, errResponsePathNotString)
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
				return nil, fmt.Errorf("field %q not found in LLM response: %w", part, errResponseFieldNotFound)
			}

			cur = next
		case []interface{}:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("invalid array index %q in LLM response: %w", part, errInvalidResponsePath)
			}

			cur = node[idx]
		default:
			return nil, fmt.Errorf("cannot descend into %q: unexpected node type: %w", part, errInvalidResponsePath)
		}
	}

	return cur, nil
}
