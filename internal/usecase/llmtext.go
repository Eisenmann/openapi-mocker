package usecase

import "strings"

// extractJSON pulls JSON out of the model's response, even if the model wrapped
// it in a ```json ... ``` markdown block or added explanatory text around it.
// This is part of the business rule "how to interpret an LLM response", so it
// lives in the usecase layer rather than in a specific LLM-provider adapter.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	start := strings.IndexAny(s, "{[")
	end := strings.LastIndexAny(s, "}]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
