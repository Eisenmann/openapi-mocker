package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// matchKey is the reserved key of a mockResponses entry that makes the entry
// conditional on the tool call's arguments.
const matchKey = "match"

var errMatchValueMissing = errors.New(`a condition needs a "value" unless its op is exists or not_exists`)

var errMatchShape = errors.New(`"match" must be an object of argument: value pairs or an array of conditions`)

// matchSpec is one condition as written in a manifest: Name is a JSON path
// inside the tool arguments.
type matchSpec struct {
	Name  string          `json:"name"`
	Path  string          `json:"path"`
	Op    domain.MatchOp  `json:"op"`
	Value json.RawMessage `json:"value"`
}

// splitMatch separates a mockResponses entry into its conditions and the
// response itself (the entry without its "match" and "fault" keys). Entries
// with neither are returned unchanged with no conditions.
//
// "match" is either {"city": "Oslo", "units.temp": "c"} (all equal) or an
// array of {"name": "city", "op": "regex", "value": "^O"} conditions.
func splitMatch(raw json.RawMessage) (conds []domain.MatchCondition, body json.RawMessage, err error) {
	var obj map[string]json.RawMessage

	if json.Unmarshal(raw, &obj) != nil {
		return nil, raw, nil //nolint:nilerr // not an object: it has no conditions.
	}

	m, hasMatch := obj[matchKey]
	_, hasFault := obj[faultKey]

	if !hasMatch && !hasFault {
		return nil, raw, nil
	}

	delete(obj, matchKey)
	delete(obj, faultKey)

	body, err = json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("re-encode response: %w", err)
	}

	if !hasMatch {
		return nil, body, nil
	}

	conds, err = parseConditions(m)
	if err != nil {
		return nil, nil, err
	}

	return conds, body, nil
}

func parseConditions(raw json.RawMessage) ([]domain.MatchCondition, error) {
	var (
		conds []domain.MatchCondition
		err   error
	)

	trimmed := bytes.TrimSpace(raw)

	switch {
	case bytes.HasPrefix(trimmed, []byte("{")):
		conds, err = parseMatchObject(trimmed)
	case bytes.HasPrefix(trimmed, []byte("[")):
		conds, err = parseMatchArray(trimmed)
	default:
		err = errMatchShape
	}

	if err != nil {
		return nil, err
	}

	for i := range conds {
		err = conds[i].Validate()
		if err != nil {
			return nil, fmt.Errorf("match condition %d: %w", i+1, err)
		}
	}

	return conds, nil
}

// parseMatchObject reads {"city": "Oslo"}: every pair must be equal.
func parseMatchObject(raw []byte) ([]domain.MatchCondition, error) {
	var pairs map[string]json.RawMessage

	err := json.Unmarshal(raw, &pairs)
	if err != nil {
		return nil, errMatchShape
	}

	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}

	sort.Strings(names)

	conds := make([]domain.MatchCondition, 0, len(names))
	for _, name := range names {
		conds = append(conds, newCondition(name, domain.OpEquals, pairs[name]))
	}

	return conds, nil
}

// parseMatchArray reads [{"name": "city", "op": "regex", "value": "^O"}].
func parseMatchArray(raw []byte) ([]domain.MatchCondition, error) {
	var specs []matchSpec

	err := json.Unmarshal(raw, &specs)
	if err != nil {
		return nil, errMatchShape
	}

	conds := make([]domain.MatchCondition, 0, len(specs))
	for i, sp := range specs {
		if len(sp.Value) == 0 && sp.Op != domain.OpExists && sp.Op != domain.OpNotExists {
			return nil, fmt.Errorf("match condition %d: %w", i+1, errMatchValueMissing)
		}

		conds = append(conds, newCondition(orDefault(sp.Name, sp.Path), sp.Op, sp.Value))
	}

	return conds, nil
}

// newCondition builds a body condition; a JSON value is compared by its text.
func newCondition(name string, op domain.MatchOp, value json.RawMessage) domain.MatchCondition {
	var v any

	dec := json.NewDecoder(bytes.NewReader(value))
	dec.UseNumber()

	if len(value) > 0 && dec.Decode(&v) != nil {
		v = string(value)
	}

	return domain.MatchCondition{Source: domain.MatchBody, Name: name, Op: op, Value: domain.ValueText(v)}
}

// matchesArguments reports whether all conditions hold for the tool arguments.
func matchesArguments(conds []domain.MatchCondition, args json.RawMessage) bool {
	var doc any

	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()

	if len(args) == 0 || dec.Decode(&doc) != nil {
		doc = nil
	}

	for i := range conds {
		v, found := domain.LookupJSONPath(doc, conds[i].Name)
		if !conds[i].Matches(v, found) {
			return false
		}
	}

	return true
}
