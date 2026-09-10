// Package router provides the default LanguageRouter implementation that
// selects a generation strategy for each language based on routing rules.
package router

import (
	"sort"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

// Priority constants for routing rules. Higher values take precedence.
const (
	priorityHighest = 100
	priorityHigh    = 90
	priorityMedium  = 80
	priorityLow     = 70
)

// DefaultLanguageRouter selects a generation strategy for a language based
// on a set of priority-ordered routing rules.
type DefaultLanguageRouter struct {
	rules []RoutingRule
}

// RoutingRule associates a language and condition with a strategy.
type RoutingRule struct {
	Language  ports.Language
	Condition func(req *ports.GenerationRequest) bool
	Strategy  ports.GenerationStrategy
	Priority  int
}

// NewDefaultLanguageRouter creates the default router with built-in rules.
func NewDefaultLanguageRouter() *DefaultLanguageRouter {
	return &DefaultLanguageRouter{
		rules: []RoutingRule{
			// Go always uses native.
			{
				Language:  ports.LanguageGo,
				Condition: func(_ *ports.GenerationRequest) bool { return true },
				Strategy:  ports.StrategyNative,
				Priority:  priorityHighest,
			},
			// TypeScript uses native generation.
			{
				Language:  ports.LanguageTypeScript,
				Condition: func(_ *ports.GenerationRequest) bool { return true },
				Strategy:  ports.StrategyNative,
				Priority:  priorityHigh,
			},
			// Python uses native generation.
			{
				Language:  ports.LanguagePython,
				Condition: func(_ *ports.GenerationRequest) bool { return true },
				Strategy:  ports.StrategyNative,
				Priority:  priorityHigh,
			},
			// C# uses native generation.
			{
				Language:  ports.LanguageCSharp,
				Condition: func(_ *ports.GenerationRequest) bool { return true },
				Strategy:  ports.StrategyNative,
				Priority:  priorityHigh,
			},
			// Java uses native generation.
			{
				Language:  ports.LanguageJava,
				Condition: func(_ *ports.GenerationRequest) bool { return true },
				Strategy:  ports.StrategyNative,
				Priority:  priorityHigh,
			},
			// Rust uses native generation.
			{
				Language:  ports.LanguageRust,
				Condition: func(_ *ports.GenerationRequest) bool { return true },
				Strategy:  ports.StrategyNative,
				Priority:  priorityHigh,
			},
		},
	}
}

// SelectStrategy returns the highest-priority strategy for the language
// whose condition matches the request.
func (r *DefaultLanguageRouter) SelectStrategy(
	language ports.Language,
	req *ports.GenerationRequest,
) ports.GenerationStrategy {
	var matched []RoutingRule

	for _, rule := range r.rules {
		if rule.Language == language && rule.Condition(req) {
			matched = append(matched, rule)
		}
	}

	if len(matched) == 0 {
		return ports.StrategyOpenAPI // sensible default.
	}

	// Sort by priority.
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Priority > matched[j].Priority
	})

	return matched[0].Strategy
}
