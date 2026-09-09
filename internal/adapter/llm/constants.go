package llm

import (
	"errors"
	"time"
)

// HTTP client timeout for all LLM provider requests. LLM responses can be
// slow (streaming token generation, rate limiting), so a generous default
// is used.
const defaultLLMTimeout = 60 * time.Second

// JSON request field names used by OpenAI-compatible, Anthropic and Google
// Gemini APIs.
const (
	roleField    = "role"
	contentField = "content"
	partsField   = "parts"
	systemRole   = "system"
	userRole     = "user"
)

// maxSuccessHTTPStatus is the upper bound (exclusive) for HTTP status codes
// considered successful. Providers return >= 300 for errors.
const maxSuccessHTTPStatus = 300

// Sentinel errors for LLM provider failures.
var (
	errProviderRequestFailed = errors.New("request to LLM provider failed")
	errProviderEmptyResponse = errors.New("LLM provider returned an empty response")
	errResponseFieldNotFound = errors.New("response field not found")
	errResponsePathNotString = errors.New("response field is not a string")
	errInvalidResponsePath   = errors.New("invalid response path")
)
