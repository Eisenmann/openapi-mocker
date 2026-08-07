package domain

import "time"

// LLMProvider - configurable LLM connection (global or per-project).
// The domain itself does not know how to call a concrete API - this knowledge
// lives behind the usecase.LLMGateway port, implemented in internal/adapter/llm.
type LLMProvider struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"projectId,omitempty"` // empty = available to all projects.
	Name          string            `json:"name"`
	Type          string            `json:"type"` // openai, azure_openai, anthropic, google, ollama, custom...
	BaseURL       string            `json:"baseUrl"`
	APIKey        string            `json:"apiKey,omitempty"`
	Model         string            `json:"model"`
	IsDefault     bool              `json:"isDefault"`
	CustomBody    string            `json:"customBody,omitempty"` // request body template for type=custom.
	CustomPath    string            `json:"customPath,omitempty"` // dot-path for extracting text from response.
	CustomHeaders map[string]string `json:"customHeaders,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
}
