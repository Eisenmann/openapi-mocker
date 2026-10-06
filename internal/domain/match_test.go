package domain_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func TestMatchCondition_Matches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		cond  domain.MatchCondition
		v     any
		found bool
		want  bool
	}{
		{"equals", domain.MatchCondition{Op: domain.OpEquals, Value: "a"}, "a", true, true},
		{"default op is equals", domain.MatchCondition{Value: "a"}, "a", true, true},
		{"equals number text", domain.MatchCondition{Value: "5"}, json.Number("5"), true, true},
		{"equals bool text", domain.MatchCondition{Value: "true"}, true, true, true},
		{"equals mismatch", domain.MatchCondition{Value: "a"}, "b", true, false},
		{"equals missing", domain.MatchCondition{Value: ""}, nil, false, false},
		{"not equals", domain.MatchCondition{Op: domain.OpNotEquals, Value: "a"}, "b", true, true},
		{"not equals missing", domain.MatchCondition{Op: domain.OpNotEquals, Value: "a"}, nil, false, true},
		{"contains", domain.MatchCondition{Op: domain.OpContains, Value: "ell"}, "hello", true, true},
		{"regex", domain.MatchCondition{Op: domain.OpRegex, Value: "^h.*o$"}, "hello", true, true},
		{"regex miss", domain.MatchCondition{Op: domain.OpRegex, Value: "^x"}, "hello", true, false},
		{"exists", domain.MatchCondition{Op: domain.OpExists}, "x", true, true},
		{"exists null", domain.MatchCondition{Op: domain.OpExists}, nil, true, false},
		{"exists missing", domain.MatchCondition{Op: domain.OpExists}, nil, false, false},
		{"not exists missing", domain.MatchCondition{Op: domain.OpNotExists}, nil, false, true},
		{"not exists null", domain.MatchCondition{Op: domain.OpNotExists}, nil, true, true},
		{"not exists present", domain.MatchCondition{Op: domain.OpNotExists}, "x", true, false},
	}

	for _, tc := range cases {
		if got := tc.cond.Matches(tc.v, tc.found); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMatchCondition_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cond domain.MatchCondition
		want error
	}{
		{"ok body path", domain.MatchCondition{Source: domain.MatchBody, Name: "a.b", Value: "x"}, nil},
		{"ok whole body", domain.MatchCondition{Source: domain.MatchBody, Op: domain.OpExists}, nil},
		{"ok header", domain.MatchCondition{Source: domain.MatchHeader, Name: "X-A", Value: "x"}, nil},
		{"bad source", domain.MatchCondition{Source: "cookie", Name: "a"}, domain.ErrMatchSource},
		{"missing source", domain.MatchCondition{Name: "a"}, domain.ErrMatchSource},
		{"query needs name", domain.MatchCondition{Source: domain.MatchQuery, Value: "x"}, domain.ErrMatchName},
		{"bad op", domain.MatchCondition{Source: domain.MatchBody, Op: "like"}, domain.ErrMatchOp},
		{"bad regex", domain.MatchCondition{Source: domain.MatchBody, Op: domain.OpRegex, Value: "("}, domain.ErrMatchRegex},
	}

	for _, tc := range cases {
		if err := tc.cond.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestLookupJSONPath(t *testing.T) {
	t.Parallel()

	var doc any

	_ = json.Unmarshal([]byte(`{"a":{"b":[{"c":1},{"c":null}]},"z":null}`), &doc)

	if v, ok := domain.LookupJSONPath(doc, "a.b[0].c"); !ok || v != float64(1) {
		t.Errorf("a.b[0].c: %v %v", v, ok)
	}

	if v, ok := domain.LookupJSONPath(doc, "a.b[1].c"); !ok || v != nil {
		t.Errorf("null leaf must be found: %v %v", v, ok)
	}

	for _, p := range []string{"a.x", "a.b[5]", "a.b.c", "z.q"} {
		if _, ok := domain.LookupJSONPath(doc, p); ok {
			t.Errorf("%s must not be found", p)
		}
	}

	if _, ok := domain.LookupJSONPath(doc, ""); !ok {
		t.Error("empty path is the document")
	}
}
