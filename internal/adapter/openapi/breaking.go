package openapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	maxChanges      = 500
	maxSchemaDepth  = 12
	sideRequest     = true
	sideResponse    = false
	statusSuccessLo = 200
	statusSuccessHi = 299
)

var _ interface {
	BreakingChanges(oldRaw, newRaw string) ([]domain.ContractChange, error)
} = (*Engine)(nil)

// BreakingChanges compares two OpenAPI documents and lists the differences
// that affect clients, breaking ones and warnings, ordered by operation.
// Requests and responses are judged from the client's side: a new required
// request field breaks clients, a removed response field breaks clients, and
// the reverse changes are compatible.
func (e *Engine) BreakingChanges(oldRaw, newRaw string) ([]domain.ContractChange, error) {
	oldDoc, err := parse([]byte(oldRaw))
	if err != nil {
		return nil, fmt.Errorf("old contract: %w", err)
	}

	newDoc, err := parse([]byte(newRaw))
	if err != nil {
		return nil, fmt.Errorf("new contract: %w", err)
	}

	c := &comparer{out: nil, op: "", where: "", seen: map[[2]*openapi3.Schema]bool{}}
	c.compareDocs(oldDoc, newDoc)

	return c.out, nil
}

type comparer struct {
	out   []domain.ContractChange
	op    string // the operation being compared.
	where string // the location prefix inside the operation.
	seen  map[[2]*openapi3.Schema]bool
}

func (c *comparer) add(kind string, sev domain.ChangeSeverity, location, format string, args ...any) {
	if len(c.out) >= maxChanges {
		return
	}

	c.out = append(c.out, domain.ContractChange{
		Kind: kind, Severity: sev, Operation: c.op, Location: location, Message: fmt.Sprintf(format, args...),
	})
}

func (c *comparer) compareDocs(oldDoc, newDoc *openapi3.T) {
	oldOps, newOps := operations(oldDoc), operations(newDoc)

	keys := make([]string, 0, len(oldOps))
	for k := range oldOps {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, key := range keys {
		c.op = key

		newOp, ok := newOps[key]
		if !ok {
			c.add(domain.ChangeOperationRemoved, domain.SeverityBreaking, "", "operation %s was removed", key)

			continue
		}

		c.compareOperation(oldOps[key], newOp)
	}
}

// opWithParams is an operation together with the parameters it inherits from
// its path item.
type opWithParams struct {
	op     *openapi3.Operation
	params map[string]*openapi3.Parameter
}

// operations indexes the document's operations by "METHOD /path".
func operations(doc *openapi3.T) map[string]opWithParams {
	out := map[string]opWithParams{}

	if doc.Paths == nil {
		return out
	}

	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			params := map[string]*openapi3.Parameter{}

			for _, group := range []openapi3.Parameters{item.Parameters, op.Parameters} {
				for _, p := range group {
					if p != nil && p.Value != nil {
						params[p.Value.In+" "+p.Value.Name] = p.Value
					}
				}
			}

			out[method+" "+path] = opWithParams{op: op, params: params}
		}
	}

	return out
}

func (c *comparer) compareOperation(oldOp, newOp opWithParams) {
	c.compareParams(oldOp.params, newOp.params)
	c.compareBody(oldOp.op.RequestBody, newOp.op.RequestBody)
	c.compareResponses(oldOp.op.Responses, newOp.op.Responses)
}

func (c *comparer) compareParams(oldP, newP map[string]*openapi3.Parameter) {
	for _, key := range sortedKeys(oldP) {
		o := oldP[key]
		loc := fmt.Sprintf("%s parameter %s", o.In, o.Name)

		n, ok := newP[key]
		if !ok {
			c.add(domain.ChangeParamRemoved, domain.SeverityWarning, loc, "%s was removed; the server no longer reads it", loc)

			continue
		}

		if !o.Required && n.Required {
			c.add(domain.ChangeParamRequired, domain.SeverityBreaking, loc, "%s became required", loc)
		}

		if ot, nt := schemaType(o.Schema), schemaType(n.Schema); ot != nt && ot != "" && nt != "" {
			c.add(domain.ChangeTypeChanged, domain.SeverityBreaking, loc, "%s changed type from %s to %s", loc, ot, nt)
		}
	}

	c.compareAddedParams(oldP, newP)
}

func (c *comparer) compareAddedParams(oldP, newP map[string]*openapi3.Parameter) {
	for _, key := range sortedKeys(newP) {
		if _, ok := oldP[key]; !ok && newP[key].Required {
			n := newP[key]
			loc := fmt.Sprintf("%s parameter %s", n.In, n.Name)
			c.add(domain.ChangeParamAddedRequired, domain.SeverityBreaking, loc, "new required %s", loc)
		}
	}
}

func (c *comparer) compareBody(oldRef, newRef *openapi3.RequestBodyRef) {
	oldBody, newBody := bodyOf(oldRef), bodyOf(newRef)

	switch {
	case oldBody == nil && newBody == nil:
		return
	case oldBody == nil:
		if newBody.Required {
			c.add(domain.ChangeBodyRequired, domain.SeverityBreaking, "request body", "a required request body was added")
		}

		return
	case newBody == nil:
		c.add(domain.ChangeBodyRemoved, domain.SeverityWarning, "request body",
			"the request body was removed; the server no longer reads it")

		return
	}

	if !oldBody.Required && newBody.Required {
		c.add(domain.ChangeBodyRequired, domain.SeverityBreaking, "request body", "the request body became required")
	}

	c.compareContent(oldBody.Content, newBody.Content, "request body", sideRequest, false)
}

func bodyOf(ref *openapi3.RequestBodyRef) *openapi3.RequestBody {
	if ref == nil {
		return nil
	}

	return ref.Value
}

func (c *comparer) compareResponses(oldR, newR *openapi3.Responses) {
	if oldR == nil {
		return
	}

	oldMap := oldR.Map()
	newMap := map[string]*openapi3.ResponseRef{}

	if newR != nil {
		newMap = newR.Map()
	}

	for _, code := range sortedKeys(oldMap) {
		loc := "response " + code
		success := isSuccess(code)

		n, ok := newMap[code]
		if !ok {
			sev := domain.SeverityWarning
			if success {
				sev = domain.SeverityBreaking
			}

			c.add(domain.ChangeResponseRemoved, sev, loc, "%s was removed", loc)

			continue
		}

		if oldMap[code].Value == nil || n.Value == nil {
			continue
		}

		c.compareContent(oldMap[code].Value.Content, n.Value.Content, loc, sideResponse, success)
	}
}

func isSuccess(code string) bool {
	var n int

	_, err := fmt.Sscanf(code, "%d", &n)

	return err == nil && n >= statusSuccessLo && n <= statusSuccessHi
}

// compareContent compares the media types both versions declare. A media type
// that disappears from a successful response breaks clients that asked for it.
func (c *comparer) compareContent(oldC, newC openapi3.Content, loc string, request, success bool) {
	for _, mt := range sortedKeys(oldC) {
		where := loc + " " + mt

		n, ok := newC[mt]
		if !ok {
			if !request && success {
				c.add(domain.ChangeMediaTypeRemoved, domain.SeverityBreaking, where, "%s is no longer offered", where)
			}

			continue
		}

		if oldC[mt].Schema == nil || n.Schema == nil {
			continue
		}

		c.where = where
		c.compareSchema("", oldC[mt].Schema.Value, n.Schema.Value, request, 0)
	}
}

func (c *comparer) location(path string) string {
	if path == "" {
		return c.where
	}

	return c.where + ": " + path
}

func (c *comparer) compareSchema(path string, oldS, newS *openapi3.Schema, request bool, depth int) {
	if oldS == nil || newS == nil || depth > maxSchemaDepth {
		return
	}

	pair := [2]*openapi3.Schema{oldS, newS}
	if c.seen[pair] {
		return
	}

	c.seen[pair] = true
	defer delete(c.seen, pair)

	if ot, nt := typeName(oldS), typeName(newS); ot != nt && ot != "" && nt != "" {
		c.add(domain.ChangeTypeChanged, domain.SeverityBreaking, c.location(path),
			"%s changed type from %s to %s", describe(path), ot, nt)

		return
	}

	c.compareEnum(path, oldS, newS, request)
	c.compareProperties(path, oldS, newS, request, depth)

	if oldS.Items != nil && newS.Items != nil {
		c.compareSchema(path+"[]", oldS.Items.Value, newS.Items.Value, request, depth+1)
	}
}

func (c *comparer) compareProperties(path string, oldS, newS *openapi3.Schema, request bool, depth int) {
	oldProps, oldReq := properties(oldS)
	newProps, newReq := properties(newS)

	for _, name := range sortedKeys(oldProps) {
		child := join(path, name)

		n, ok := newProps[name]
		if !ok {
			sev := domain.SeverityBreaking
			if request {
				sev = domain.SeverityWarning // the server just ignores an input it no longer knows.
			}

			c.add(domain.ChangeFieldRemoved, sev, c.location(child), "field %s was removed", child)

			continue
		}

		c.compareRequiredness(child, request, oldReq[name], newReq[name])
		c.compareSchema(child, oldProps[name], n, request, depth+1)
	}

	if !request {
		return
	}

	for _, name := range sortedKeys(newProps) {
		if _, ok := oldProps[name]; !ok && newReq[name] {
			c.add(domain.ChangeFieldAddedRequired, domain.SeverityBreaking, c.location(join(path, name)),
				"new required field %s", join(path, name))
		}
	}
}

// compareRequiredness: a request field that becomes required breaks senders; a
// response field that becomes optional breaks readers.
func (c *comparer) compareRequiredness(field string, request, wasRequired, isRequired bool) {
	switch {
	case request && !wasRequired && isRequired:
		c.add(domain.ChangeFieldRequired, domain.SeverityBreaking, c.location(field), "field %s became required", field)
	case !request && wasRequired && !isRequired:
		c.add(domain.ChangeFieldOptional, domain.SeverityBreaking, c.location(field),
			"field %s became optional; clients may no longer rely on it", field)
	}
}

// compareEnum: narrowing the values a client may send breaks it; widening the
// values a client may receive can break a client that switches on them.
func (c *comparer) compareEnum(path string, oldS, newS *openapi3.Schema, request bool) {
	if len(oldS.Enum) == 0 || len(newS.Enum) == 0 {
		return
	}

	oldSet, newSet := enumSet(oldS.Enum), enumSet(newS.Enum)

	if request {
		for _, v := range sortedKeys(oldSet) {
			if !newSet[v] {
				c.add(domain.ChangeEnumRemoved, domain.SeverityBreaking, c.location(path),
					"%s no longer accepts %q", describe(path), v)
			}
		}

		return
	}

	for _, v := range sortedKeys(newSet) {
		if !oldSet[v] {
			c.add(domain.ChangeEnumAdded, domain.SeverityWarning, c.location(path),
				"%s may now be %q", describe(path), v)
		}
	}
}

// properties collects the properties of a schema, including those from allOf,
// and the names that are required.
func properties(s *openapi3.Schema) (props map[string]*openapi3.Schema, required map[string]bool) {
	props, required = map[string]*openapi3.Schema{}, map[string]bool{}

	collectProperties(s, props, required, 0)

	return props, required
}

func collectProperties(s *openapi3.Schema, props map[string]*openapi3.Schema, required map[string]bool, depth int) {
	if s == nil || depth > maxSchemaDepth {
		return
	}

	for _, sub := range s.AllOf {
		if sub != nil {
			collectProperties(sub.Value, props, required, depth+1)
		}
	}

	for name, ref := range s.Properties {
		if ref != nil {
			props[name] = ref.Value
		}
	}

	for _, name := range s.Required {
		required[name] = true
	}
}

func typeName(s *openapi3.Schema) string {
	if s == nil || s.Type == nil {
		return ""
	}

	types := append([]string(nil), s.Type.Slice()...)
	sort.Strings(types)

	return strings.Join(types, "|")
}

func schemaType(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}

	return typeName(ref.Value)
}

func enumSet(values []any) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[fmt.Sprint(v)] = true
	}

	return out
}

func join(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}

func describe(path string) string {
	if path == "" {
		return "the body"
	}

	return path
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
