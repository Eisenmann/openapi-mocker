package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MatchSource says where a condition looks for its value in the request.
type MatchSource string

const (
	MatchBody   MatchSource = "body"   // a JSON path in the request body.
	MatchQuery  MatchSource = "query"  // a query parameter.
	MatchHeader MatchSource = "header" // a request header.
	MatchPath   MatchSource = "path"   // a path parameter of the route.
)

// MatchOp is the comparison a condition makes.
type MatchOp string

const (
	OpEquals    MatchOp = "equals"
	OpNotEquals MatchOp = "not_equals"
	OpContains  MatchOp = "contains"
	OpRegex     MatchOp = "regex"
	OpExists    MatchOp = "exists"
	OpNotExists MatchOp = "not_exists"
)

// MatchCondition is one test on a request. A rule's conditions must all hold
// for the rule to be picked.
type MatchCondition struct {
	Source MatchSource `json:"source"`
	// Name is the query parameter, header or path parameter name, or the JSON
	// path inside the body ("user.role", "items[0].id").
	Name string  `json:"name"`
	Op   MatchOp `json:"op"`
	// Value is compared as text (numbers and booleans in a body are compared
	// by their JSON text). Unused by exists and not_exists.
	Value string `json:"value"`
}

// Errors returned by MatchCondition.Validate.
var (
	ErrMatchSource = errors.New("match source must be one of: body, query, header, path")
	ErrMatchName   = errors.New("match name is required")
	ErrMatchOp     = errors.New("match op must be one of: equals, not_equals, contains, regex, exists, not_exists")
	ErrMatchValue  = errors.New("match value is required")
	ErrMatchRegex  = errors.New("match value is not a valid regular expression")
)

// Normalized returns the condition with its defaults applied: the op
// defaults to equals.
func (c MatchCondition) Normalized() MatchCondition {
	if c.Op == "" {
		c.Op = OpEquals
	}

	return c
}

// Validate checks that the condition is well-formed. A body condition may
// leave Name empty to address the whole body.
func (c MatchCondition) Validate() error {
	c = c.Normalized()

	switch c.Source {
	case MatchBody:
	case MatchQuery, MatchHeader, MatchPath:
		if strings.TrimSpace(c.Name) == "" {
			return ErrMatchName
		}
	default:
		return ErrMatchSource
	}

	switch c.Op {
	case OpExists, OpNotExists:
		return nil
	case OpEquals, OpNotEquals, OpContains:
	case OpRegex:
		_, err := regexp.Compile(c.Value)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrMatchRegex, err)
		}
	default:
		return ErrMatchOp
	}

	return nil
}

// Matches reports whether the value found in the request satisfies the
// condition. found says whether the value is present at all; a JSON null
// counts as present but does not satisfy exists.
func (c MatchCondition) Matches(v any, found bool) bool {
	c = c.Normalized()

	switch c.Op {
	case OpExists:
		return found && v != nil
	case OpNotExists:
		return !found || v == nil
	case OpNotEquals:
		return !found || ValueText(v) != c.Value
	case OpEquals, OpContains, OpRegex:
		return found && c.compare(ValueText(v))
	default:
		return false
	}
}

// compare applies the positive text comparisons.
func (c MatchCondition) compare(text string) bool {
	switch c.Op {
	case OpEquals:
		return text == c.Value
	case OpContains:
		return strings.Contains(text, c.Value)
	case OpRegex:
		re, err := regexp.Compile(c.Value)

		return err == nil && re.MatchString(text)
	case OpNotEquals, OpExists, OpNotExists:
		return false
	default:
		return false
	}
}

// ValueText renders a decoded JSON value (or a Go primitive) as plain text;
// objects and arrays become compact JSON, nil becomes "".
func ValueText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		var buf bytes.Buffer

		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)

		//nolint:errchkjson // v is decoded JSON; it always encodes.
		if enc.Encode(v) != nil {
			return ""
		}

		return strings.TrimRight(buf.String(), "\n")
	}
}

// LookupJSONPath returns the value at a dotted path ("user.name",
// "items[0].id") inside a decoded JSON document; an empty path is the
// document itself. found is false when any step is missing.
func LookupJSONPath(doc any, path string) (v any, found bool) {
	cur := doc

	for _, seg := range strings.FieldsFunc(path, func(r rune) bool { return r == '.' || r == '[' || r == ']' }) {
		cur, found = descend(cur, seg)
		if !found {
			return nil, false
		}
	}

	return cur, true
}

// descend follows one path segment into an object or array.
func descend(cur any, seg string) (v any, found bool) {
	switch x := cur.(type) {
	case map[string]any:
		v, found = x[seg]

		return v, found
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(x) {
			return nil, false
		}

		return x[i], true
	default:
		return nil, false
	}
}
