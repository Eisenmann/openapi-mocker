// Package graphql is the interface adapter encapsulating a specific GraphQL
// parsing/validation library (gqlparser). It exposes only GraphQLEngine,
// implemented with usecase-layer primitive types — no gqlparser type ever
// leaves this package.
//
// It implements the GraphQL spec semantics
// (https://github.com/graphql/graphql-spec): schema validation, query
// parse+validate, and mock response generation shaped by the query's
// selection set (aliases, fragments, inline fragments, args, variables).
package graphql

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// MaxCachedSchemas bounds the schema cache so long-lived servers don't
// accumulate an unbounded number of compiled schemas as contracts are
// republished. When the cap is reached, the least-recently-used entry is
// evicted.
const MaxCachedSchemas = 64

// Engine implements usecase.GraphQLEngine using gqlparser. It caches
// compiled schemas keyed by content hash so repeated requests for the same
// contract don't re-parse + re-validate on every call. The cache is bounded
// (LRU) to prevent unbounded memory growth on long-lived servers.
type Engine struct {
	mu      sync.Mutex
	schemas map[string]*ast.Schema // key: sha256(raw); value: *ast.Schema.
	order   []string               // LRU order: front = most recently used.
}

func NewEngine() *Engine {
	return &Engine{schemas: make(map[string]*ast.Schema)}
}

var _ usecase.GraphQLEngine = (*Engine)(nil)

// CachedSchemaCount returns the number of compiled schemas currently held in
// the cache. It is safe for concurrent use and is also handy for
// observability/metrics in long-lived servers.
func (e *Engine) CachedSchemaCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return len(e.schemas)
}

// formatGQLError produces a friendly string for a single gqlparser error.
func formatGQLError(ge *gqlerror.Error) string {
	msg := ge.Message
	if len(ge.Locations) > 0 {
		msg = fmt.Sprintf("%s (line %d, col %d)",
			msg, ge.Locations[0].Line, ge.Locations[0].Column)
	}

	return msg
}

// validationMessages extracts friendly error messages from any error value.
func validationMessages(err error) []string {
	var lst gqlerror.List
	if errors.As(err, &lst) {
		out := make([]string, 0, len(lst))
		for _, ge := range lst {
			out = append(out, formatGQLError(ge))
		}

		return out
	}

	var ge *gqlerror.Error
	if errors.As(err, &ge) {
		return []string{formatGQLError(ge)}
	}

	return []string{err.Error()}
}

// Validate parses and validates an SDL schema document.
func (e *Engine) Validate(raw []byte) usecase.GraphQLValidationResult {
	s, err := e.loadSchema(raw)
	if err != nil {
		return usecase.GraphQLValidationResult{
			Valid:  false,
			Errors: validationMessages(err),
		}
	}

	return usecase.GraphQLValidationResult{
		Valid:          true,
		TypeCount:      cntTypes(s),
		QueryFields:    cntFields(s.Query),
		MutationFields: cntFields(s.Mutation),
	}
}

func (e *Engine) ParseAndValidate(raw []byte) error {
	_, err := e.loadSchema(raw)
	return err
}

func cntTypes(s *ast.Schema) int {
	n := 0

	for _, d := range s.Types {
		if !d.BuiltIn {
			n++
		}
	}

	return n
}

func cntFields(d *ast.Definition) int {
	if d == nil {
		return 0
	}

	n := 0

	for _, f := range d.Fields {
		if !strings.HasPrefix(f.Name, "__") {
			n++
		}
	}

	return n
}

// Introspection returns SDL of the schema (custom types only, no built-ins).
func (e *Engine) Introspection(raw []byte) (string, error) {
	s, err := e.loadSchema(raw)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	formatter.NewFormatter(&buf,
		formatter.WithIndent("  "),
	).FormatSchema(s)

	return strings.TrimSpace(buf.String()), nil
}

// ListOperations enumerates the root query/mutation/subscription fields.
func (e *Engine) ListOperations(raw []byte) ([]usecase.GraphQLOperation, error) {
	s, err := e.loadSchema(raw)
	if err != nil {
		return nil, err
	}

	opTypes := []struct {
		opType string
		def    *ast.Definition
	}{
		{"query", s.Query},
		{"mutation", s.Mutation},
		{"subscription", s.Subscription},
	}

	var out []usecase.GraphQLOperation

	for _, ot := range opTypes {
		if ot.def == nil {
			continue
		}

		for _, fld := range ot.def.Fields {
			if strings.HasPrefix(fld.Name, "__") {
				continue
			}

			var args []usecase.GraphQLArgument
			for _, a := range fld.Arguments {
				args = append(args, usecase.GraphQLArgument{
					Name:       a.Name,
					Type:       a.Type.String(),
					HasDefault: a.DefaultValue != nil,
				})
			}

			out = append(out, usecase.GraphQLOperation{
				Type:        ot.opType,
				Name:        fld.Name,
				Description: firstNonEmpty(fld.Description, fld.Name),
				ReturnType:  fld.Type.String(),
				Arguments:   args,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}

		return out[i].Name < out[j].Name
	})

	return out, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}

	return ""
}

// Execute validates a query against the schema and returns a mock JSON body.
// Query parse/validation errors are returned as *usecase.GraphQLQueryError.
func (e *Engine) Execute(raw []byte, query, opName string,
	vars map[string]any,
) ([]byte, error) {
	s, err := e.loadSchema(raw)
	if err != nil {
		return nil, err
	}

	qd, err := parser.ParseQuery(&ast.Source{Name: "query.graphql", Input: query})
	if err != nil {
		return nil, queryErrorFromError(err)
	}

	op, err := selectOperation(qd, opName)
	if err != nil {
		return nil, err
	}

	// Index fragment definitions by name.
	frags := make(map[string]*ast.FragmentDefinition, len(qd.Fragments))
	for _, fr := range qd.Fragments {
		frags[fr.Name] = fr
	}

	// Validate against schema (full spec) BEFORE any custom traversal. The
	// default rules reject cyclic fragment spreads ("cannot spread fragment
	// within itself"), unknown fragments, and other document-level errors;
	// running this first means malformed documents get a clean
	// *GraphQLQueryError instead of risking a stack overflow in our own scan.
	verr := validator.ValidateWithRules(s, qd,
		rules.NewDefaultRules())
	if len(verr) > 0 {
		return nil, queryErrorFromList(verr)
	}

	// Reject deep documents. A hand-crafted 100k-deep selection set would
	// otherwise consume unbounded stack/CPU in the mock walker.
	if depth := selSetDepth(op.SelectionSet, frags, 0); depth > maxQueryDepth {
		return nil, queryError(
			"query exceeds maximum depth of %d (got %d)", maxQueryDepth, depth)
	}

	// Reject introspection queries. The mock engine does not implement the
	// introspection system (__schema/__type); returning mock garbage for them
	// would silently break GraphiQL/Playground/codegen tools. Point the user
	// at the SDL export endpoint instead. The scan is cycle-safe via the
	// visited set (defense in depth; validation already rejects cycles).
	if hasIntrospectionField(op.SelectionSet, frags, make(map[string]struct{})) {
		return nil, queryError(
			"introspection queries (__schema/__type) are not supported by the mock engine; " +
				"use GET /api/projects/{id}/graphql/schema for the SDL export")
	}

	// Coerce variables.
	coerced, err := coerceVariables(s, op, vars)
	if err != nil {
		return nil, err
	}

	// Root type.
	rootDef, err := rootDefinition(s, op)
	if err != nil {
		return nil, err
	}

	data := buildSelSet(s, frags, rootDef, op.SelectionSet, coerced)

	return json.MarshalIndent(map[string]any{"data": data}, "", "  ")
}

// loadSchema returns the compiled schema for raw, reusing the LRU cache. The
// cache is keyed by content hash so repeated requests for the same contract
// don't re-parse + re-validate on every call. Must be called after all
// exported Engine methods (funcorder convention).
func (e *Engine) loadSchema(raw []byte) (*ast.Schema, error) {
	sum := sha256.Sum256(raw)
	key := string(sum[:])

	e.mu.Lock()
	if s, ok := e.schemas[key]; ok {
		// Move to front (most recently used).
		for i, k := range e.order {
			if k == key {
				e.order = append(e.order[:i], e.order[i+1:]...)
				break
			}
		}

		e.order = append(e.order, key)
		e.mu.Unlock()

		return s, nil
	}
	e.mu.Unlock()

	s, err := validator.LoadSchema(
		validator.Prelude, // official built-ins, marked BuiltIn.
		&ast.Source{Name: "schema.graphql", Input: string(raw)},
	)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	// Re-check in case another goroutine stored it while we were parsing.
	if existing, ok := e.schemas[key]; ok {
		return existing, nil
	}

	e.schemas[key] = s

	e.order = append(e.order, key)
	if len(e.order) > MaxCachedSchemas {
		oldest := e.order[0]
		e.order = e.order[1:]
		delete(e.schemas, oldest)
	}

	return s, nil
}

// selectOperation picks the operation to execute: by name when given, the
// only one when a single operation is defined, otherwise an error.
func selectOperation(qd *ast.QueryDocument,
	opName string,
) (*ast.OperationDefinition, error) {
	switch {
	case opName != "":
		for _, o := range qd.Operations {
			if o.Name == opName {
				return o, nil
			}
		}

		return nil, queryError("unknown operation named %q", opName)
	case len(qd.Operations) == 1:
		return qd.Operations[0], nil
	default:
		return nil, queryError("operation name required when multiple operations are defined")
	}
}

// coerceVariables applies the schema's variable coercion to the request
// variables; an empty map is returned when the operation declares none.
func coerceVariables(s *ast.Schema, op *ast.OperationDefinition,
	vars map[string]any,
) (map[string]any, error) {
	if len(op.VariableDefinitions) == 0 {
		return map[string]any{}, nil
	}

	coerced, err := validator.VariableValues(s, op, vars)
	if err != nil {
		return nil, queryErrorFromError(err)
	}

	return coerced, nil
}

// rootDefinition returns the schema root type for the operation kind, or an
// error when the schema does not declare it.
func rootDefinition(s *ast.Schema, op *ast.OperationDefinition) (*ast.Definition, error) {
	switch op.Operation {
	case ast.Query:
		return queryRootDefinition(s, op)
	case ast.Mutation:
		return mutationRootDefinition(s, op)
	case ast.Subscription:
		return subscriptionRootDefinition(s, op)
	default:
		return nil, queryError("unsupported operation %q", op.Operation)
	}
}

// queryRootDefinition returns the schema query root type, or an error when the
// schema does not declare it.
func queryRootDefinition(s *ast.Schema, op *ast.OperationDefinition) (*ast.Definition, error) {
	if s.Query == nil {
		return nil, queryError("schema does not declare a %s root type", op.Operation)
	}

	return s.Query, nil
}

// mutationRootDefinition returns the schema mutation root type, or an error
// when the schema does not declare it.
func mutationRootDefinition(s *ast.Schema, op *ast.OperationDefinition) (*ast.Definition, error) {
	if s.Mutation == nil {
		return nil, queryError("schema does not declare a %s root type", op.Operation)
	}

	return s.Mutation, nil
}

// subscriptionRootDefinition returns the schema subscription root type, or an
// error when the schema does not declare it.
func subscriptionRootDefinition(s *ast.Schema, op *ast.OperationDefinition) (*ast.Definition, error) {
	if s.Subscription == nil {
		return nil, queryError("schema does not declare a %s root type", op.Operation)
	}

	return s.Subscription, nil
}

func queryError(format string, args ...any) *usecase.GraphQLQueryError {
	return &usecase.GraphQLQueryError{
		Errors: []usecase.GraphQLQueryErrorItem{{
			Message: fmt.Sprintf(format, args...),
		}},
	}
}

func queryErrorFromError(err error) *usecase.GraphQLQueryError {
	var ge *gqlerror.Error
	if errors.As(err, &ge) {
		return queryErrorFromGQLError(ge)
	}

	return &usecase.GraphQLQueryError{
		Errors: []usecase.GraphQLQueryErrorItem{{Message: err.Error()}},
	}
}

func queryErrorFromGQLError(ge *gqlerror.Error) *usecase.GraphQLQueryError {
	item := usecase.GraphQLQueryErrorItem{Message: ge.Message}
	for _, loc := range ge.Locations {
		item.Locations = append(item.Locations,
			usecase.GraphQLLocation{Line: loc.Line, Column: loc.Column})
	}

	return &usecase.GraphQLQueryError{Errors: []usecase.GraphQLQueryErrorItem{item}}
}

func queryErrorFromList(list gqlerror.List) *usecase.GraphQLQueryError {
	items := make([]usecase.GraphQLQueryErrorItem, 0, len(list))
	for _, ge := range list {
		item := usecase.GraphQLQueryErrorItem{Message: ge.Message}
		for _, loc := range ge.Locations {
			item.Locations = append(item.Locations,
				usecase.GraphQLLocation{Line: loc.Line, Column: loc.Column})
		}

		items = append(items, item)
	}

	return &usecase.GraphQLQueryError{Errors: items}
}

// buildSelSet builds mock values for a selection set, honoring aliases,
// fragment spreads, inline fragments, and type conditions. Fields sharing
// the same response key have their sub-selections merged per spec.
func buildSelSet(s *ast.Schema, frags map[string]*ast.FragmentDefinition,
	objDef *ast.Definition, selSet ast.SelectionSet,
	vars map[string]any,
) map[string]any {
	result := map[string]any{}

	for _, sel := range selSet {
		switch v := sel.(type) {
		case *ast.Field:
			applyField(s, frags, objDef, result, v, vars)
		case *ast.FragmentSpread:
			applyFragmentSpread(s, frags, objDef, result, v, vars)
		case *ast.InlineFragment:
			applyInlineFragment(s, frags, objDef, result, v, vars)
		}
	}

	return result
}

// applyField resolves a single field selection into result, honoring the
// alias as the response key and merging values for duplicate keys.
func applyField(s *ast.Schema, frags map[string]*ast.FragmentDefinition,
	objDef *ast.Definition, result map[string]any, v *ast.Field,
	vars map[string]any,
) {
	key := v.Alias
	if key == "" {
		key = v.Name
	}
	// __typename is implicit on every object; resolve from the
	// current type definition.
	if v.Name == "__typename" && objDef != nil {
		result[key] = objDef.Name
		return
	}

	fd := findField(objDef, v.Name)
	if fd == nil || fd.Type == nil {
		result[key] = nil
		return
	}

	args := resolveArgs(s, fd, v.Arguments, vars)
	val := fieldValue(s, frags, fd.Type, v.SelectionSet, args, vars)
	// Merge duplicate response keys: when both the existing and the
	// new value are objects (or arrays of objects), merge their
	// sub-selections recursively.
	if existing, ok := result[key]; ok {
		if merged, ok := mergeResponseValues(existing, val); ok {
			result[key] = merged
			return
		}
	}

	result[key] = val
}

// applyFragmentSpread inlines a named fragment's selections, resolving the
// fragment's type condition ("… on User") against the schema: fields inside
// resolve against the concrete type, not the parent.
func applyFragmentSpread(s *ast.Schema, frags map[string]*ast.FragmentDefinition,
	objDef *ast.Definition, result map[string]any, v *ast.FragmentSpread,
	vars map[string]any,
) {
	frag := frags[v.Name]
	if frag == nil {
		return
	}

	mergeSelSet(result,
		buildSelSet(s, frags, resolveTypeCondition(s, objDef, frag.TypeCondition), frag.SelectionSet, vars))
}

// applyInlineFragment inlines an anonymous fragment's selections, honoring
// its optional type condition.
func applyInlineFragment(s *ast.Schema, frags map[string]*ast.FragmentDefinition,
	objDef *ast.Definition, result map[string]any, v *ast.InlineFragment,
	vars map[string]any,
) {
	mergeSelSet(result,
		buildSelSet(s, frags, resolveTypeCondition(s, objDef, v.TypeCondition), v.SelectionSet, vars))
}

// resolveTypeCondition returns the schema type for a fragment type condition,
// falling back to parentDef when the condition is empty or names an unknown
// type (unknown types are already rejected by validation).
func resolveTypeCondition(s *ast.Schema, parentDef *ast.Definition,
	typeCondition string,
) *ast.Definition {
	if typeCondition == "" {
		return parentDef
	}

	if tc := s.Types[typeCondition]; tc != nil {
		return tc
	}

	return parentDef
}

// mergeResponseValues tries to merge a newly-computed field value into an
// existing one. Objects are merged recursively; arrays of objects are merged
// element-by-element (same length). Reports ok=false when values aren't
// mergeable (scalars, mismatched shapes).
func mergeResponseValues(existing, val any) (any, bool) {
	exObj, ok1 := existing.(map[string]any)

	newObj, ok2 := val.(map[string]any)
	if ok1 && ok2 {
		merged := mergeResponseMaps(exObj, newObj)
		return merged, true
	}

	exList, ok1 := existing.([]any)

	newList, ok2 := val.([]any)
	if ok1 && ok2 && len(exList) == len(newList) {
		for i := range exList {
			if exElem, o1 := exList[i].(map[string]any); o1 {
				if newElem, o2 := newList[i].(map[string]any); o2 {
					exList[i] = mergeResponseMaps(exElem, newElem)
				}
			}
		}

		return exList, true
	}

	return nil, false
}

// mergeResponseMaps recursively merges src into dst. Values with the same key
// that are both maps are merged recursively; otherwise src's value wins.
func mergeResponseMaps(dst, src map[string]any) map[string]any {
	merged := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		merged[k] = v
	}

	for k, v := range src {
		if existing, ok := merged[k]; ok {
			exObj, ok1 := existing.(map[string]any)

			newObj, ok2 := v.(map[string]any)
			if ok1 && ok2 {
				merged[k] = mergeResponseMaps(exObj, newObj)
				continue
			}
		}

		merged[k] = v
	}

	return merged
}

// mergeSelSet merges the values of src into dst (which is a response map).
func mergeSelSet(dst, src map[string]any) {
	for k, v := range src {
		if existing, ok := dst[k]; ok {
			if merged, ok := mergeResponseValues(existing, v); ok {
				dst[k] = merged
				continue
			}
		}

		dst[k] = v
	}
}

// fieldValue builds a mock value for a field, honoring list types (returns an
// array sized by the limit arg) and recursing into selection sets on composite
// types, per the GraphQL spec.
func fieldValue(s *ast.Schema, frags map[string]*ast.FragmentDefinition,
	t *ast.Type, selSet ast.SelectionSet, args,
	vars map[string]any,
) any {
	if t.Elem != nil {
		n := listSize(args)

		out := make([]any, n)
		for i := range out {
			out[i] = fieldValue(s, frags, t.Elem, selSet, args, vars)
		}

		return out
	}

	if len(selSet) > 0 {
		if def := s.Types[t.NamedType]; def != nil && isComposite(def) {
			return buildSelSet(s, frags, def, selSet, vars)
		}

		return nil
	}

	return leafValue(s, t, args)
}

// maxMockListSize is the upper bound for the mock array length produced for
// list fields. Values above it are clamped (not ignored) so callers get a
// predictable, bounded response.
const maxMockListSize = 20

// maxQueryDepth bounds the maximum nesting depth of the document's selection
// set (field→sub-selection→field…). This is a hardening measure: a hand-crafted
// deeply-nested document would otherwise consume unbounded stack/CPU in the
// mock walker. A value of 50 comfortably covers real-world queries (GraphQL
// specs commonly recommend 5–15); list/mutation queries don't count extra.
const maxQueryDepth = 50

// listSize returns the mock array length for a list field. The limit
// argument is honored when present and clamped to [1, maxMockListSize];
// gqlparser yields int64 for Int literals and float64 for JSON variables.
// When no limit is provided, a small default (2) is used.
func listSize(args map[string]any) int {
	n := 2

	if v, ok := args["limit"]; ok {
		switch x := v.(type) {
		case int:
			if x > 0 {
				n = min(x, maxMockListSize)
			}
		case int64:
			if x > 0 {
				n = int(min(x, int64(maxMockListSize)))
			}
		case float64:
			if x > 0 {
				n = int(min(x, float64(maxMockListSize)))
			}
		}
	}

	return n
}

// hasIntrospectionField reports whether any field in the selection set (or a
// nested selection set, including fragment spreads) is an introspection field
// (__schema or __type). The visited set tracks fragment names already expanded
// so cyclic fragment references (defense in depth — the validator already
// rejects them) cannot cause unbounded recursion.
func hasIntrospectionField(selSet ast.SelectionSet,
	frags map[string]*ast.FragmentDefinition,
	visited map[string]struct{},
) bool {
	for _, sel := range selSet {
		if selectionHasIntrospection(sel, frags, visited) {
			return true
		}
	}

	return false
}

// selectionHasIntrospection reports whether a single selection (or its nested
// selections/fragments) references an introspection field.
func selectionHasIntrospection(sel ast.Selection,
	frags map[string]*ast.FragmentDefinition,
	visited map[string]struct{},
) bool {
	switch v := sel.(type) {
	case *ast.Field:
		return fieldHasIntrospection(v, frags, visited)
	case *ast.FragmentSpread:
		return fragmentSpreadHasIntrospection(v, frags, visited)
	case *ast.InlineFragment:
		return hasIntrospectionField(v.SelectionSet, frags, visited)
	}

	return false
}

// fieldHasIntrospection reports whether the field itself is an introspection
// field (`__schema`/`__type`) or has nested selections that are.
func fieldHasIntrospection(v *ast.Field,
	frags map[string]*ast.FragmentDefinition,
	visited map[string]struct{},
) bool {
	return (v.Name == "__schema" || v.Name == "__type") ||
		hasIntrospectionField(v.SelectionSet, frags, visited)
}

// fragmentSpreadHasIntrospection reports whether the referenced fragment's
// selections contain an introspection field. The fragment is expanded only
// once per traversal (tracked in visited) to bound recursion.
func fragmentSpreadHasIntrospection(v *ast.FragmentSpread,
	frags map[string]*ast.FragmentDefinition,
	visited map[string]struct{},
) bool {
	frag := frags[v.Name]
	if frag == nil {
		return false
	}

	if _, seen := visited[v.Name]; seen {
		return false
	}

	visited[v.Name] = struct{}{}

	return hasIntrospectionField(frag.SelectionSet, frags, visited)
}

// selSetDepth returns the maximum selection nesting depth of a selection set,
// counting field→sub-selection edges. Fragment spreads are expanded following
// their referenced fragment's selection set. This runs AFTER schema
// validation (see Execute), which already rejects cyclic/mutually-referencing
// fragments, so the walk terminates.
func selSetDepth(selSet ast.SelectionSet,
	frags map[string]*ast.FragmentDefinition,
	depth int,
) int {
	max := depth

	for _, sel := range selSet {
		switch v := sel.(type) {
		case *ast.Field:
			if d := selSetDepth(v.SelectionSet, frags, depth+1); d > max {
				max = d
			}
		case *ast.InlineFragment:
			if d := selSetDepth(v.SelectionSet, frags, depth+1); d > max {
				max = d
			}
		case *ast.FragmentSpread:
			if frag := frags[v.Name]; frag != nil {
				if d := selSetDepth(frag.SelectionSet, frags, depth+1); d > max {
					max = d
				}
			}
		}
	}

	return max
}

func findField(def *ast.Definition, name string) *ast.FieldDefinition {
	if def == nil {
		return nil
	}

	for _, f := range def.Fields {
		if f.Name == name {
			return f
		}
	}

	return nil
}

func isComposite(d *ast.Definition) bool {
	return d.Kind == ast.Object || d.Kind == ast.Interface ||
		d.Kind == ast.Union
}

func resolveArgs(_ *ast.Schema, fd *ast.FieldDefinition,
	args ast.ArgumentList, vars map[string]any,
) map[string]any {
	out := map[string]any{}

	for _, ad := range fd.Arguments {
		if ad.DefaultValue != nil {
			v, err := ad.DefaultValue.Value(vars)
			if err == nil {
				out[ad.Name] = v
			}
		}
	}

	for _, a := range args {
		v, err := a.Value.Value(vars)
		if err == nil {
			out[a.Name] = v
		}
	}

	return out
}

func leafValue(s *ast.Schema, t *ast.Type, args map[string]any) any {
	if t.Elem != nil {
		return listLeafValue(s, t.Elem, args)
	}

	def := s.Types[t.NamedType]
	if def == nil {
		return nil
	}

	switch def.Kind {
	case ast.Scalar:
		return scalarValue(def)
	case ast.Enum:
		return enumValue(def)
	case ast.Object, ast.Interface, ast.Union:
		return objShape(s, def, args)
	case ast.InputObject:
		return inputShape(s, def, args)
	default:
		return nil
	}
}

// listLeafValue builds a mock array for a list-typed leaf, sizing it by the
// limit argument.
func listLeafValue(s *ast.Schema, t *ast.Type, args map[string]any) any {
	out := make([]any, listSize(args))
	for i := range out {
		out[i] = leafValue(s, t, args)
	}

	return out
}

// scalarValue returns a mock value for a scalar type by name.
func scalarValue(def *ast.Definition) any {
	switch def.Name {
	case "Int":
		return 1
	case "Float":
		return 1.0
	case "String":
		return "example"
	case "Boolean":
		return true
	case "ID":
		return "1"
	default:
		return "mock"
	}
}

// enumValue returns the first enum value name, or nil when none are declared.
func enumValue(def *ast.Definition) any {
	if len(def.EnumValues) > 0 {
		return def.EnumValues[0].Name
	}

	return nil
}

// objShape produces a shallow shape for a composite type with no selection
// set. Validated queries always provide sub-selections on composite fields,
// so this only runs as a defensive fallback.
func objShape(s *ast.Schema, def *ast.Definition,
	args map[string]any,
) map[string]any {
	out := map[string]any{}

	if def.Kind == ast.Union && len(def.Types) > 0 {
		if m := s.Types[def.Types[0]]; m != nil {
			return objShape(s, m, args)
		}

		return out
	}

	for _, f := range def.Fields {
		if f.Type == nil || f.Name == "__typename" {
			continue
		}

		out[f.Name] = leafValue(s, f.Type, args)
	}

	if len(out) == 0 {
		out["__typename"] = def.Name
	}

	return out
}

func inputShape(s *ast.Schema, def *ast.Definition,
	args map[string]any,
) map[string]any {
	out := map[string]any{}

	for _, f := range def.Fields {
		if f.Type == nil {
			continue
		}

		if f.DefaultValue != nil {
			v, err := f.DefaultValue.Value(args)
			if err == nil {
				out[f.Name] = v
				continue
			}
		}

		out[f.Name] = leafValue(s, f.Type, args)
	}

	if len(out) == 0 {
		out["__typename"] = def.Name
	}

	return out
}
