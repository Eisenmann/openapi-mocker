package usecase

import (
	"fmt"
	"net/textproto"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// matchesAll reports whether every condition holds for the request.
func (c *TemplateContext) matchesAll(conds []domain.MatchCondition) bool {
	for i := range conds {
		v, found := c.conditionValue(&conds[i])
		if !conds[i].Matches(v, found) {
			return false
		}
	}

	return true
}

// conditionValue looks up the request value a condition tests. Only the first
// value of a repeated query parameter or header is considered.
func (c *TemplateContext) conditionValue(cond *domain.MatchCondition) (v any, found bool) {
	switch cond.Source {
	case domain.MatchBody:
		return c.bodyLookup(cond.Name)
	case domain.MatchQuery:
		return firstOf(c.Query, cond.Name)
	case domain.MatchHeader:
		return firstOf(c.Header, textproto.CanonicalMIMEHeaderKey(cond.Name))
	case domain.MatchPath:
		val, ok := c.PathParams[cond.Name]

		return val, ok
	default:
		return nil, false
	}
}

func firstOf(m map[string][]string, name string) (v any, found bool) {
	if vals := m[name]; len(vals) > 0 {
		return vals[0], true
	}

	return nil, false
}

// validateMatch checks the conditions of a rule before it is saved.
func validateMatch(conds []domain.MatchCondition) error {
	for i := range conds {
		err := conds[i].Validate()
		if err != nil {
			return fmt.Errorf("%w: condition %d: %w", ErrInvalidMatch, i+1, err)
		}
	}

	return nil
}
