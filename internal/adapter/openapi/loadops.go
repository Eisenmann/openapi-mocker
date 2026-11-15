package openapi

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	// variantCount is how many example values or bodies an operation gets, so
	// generated load is not one identical request.
	variantCount = 8
	// bodyAttempts bounds the random draws used to collect valid body variants.
	bodyAttempts = variantCount * 4
	// coinSides is the number of outcomes of a coin flip (a random boolean).
	coinSides    = 2
	jsonMIME     = "application/json"
	defaultParam = "1"
)

// LoadOperations lists the contract's operations with valid example
// parameters and request bodies, for the load tester. The order is stable:
// by path, then by method.
func (e *Engine) LoadOperations(raw []byte) ([]usecase.LoadOperation, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}

	if doc.Paths == nil {
		return nil, nil
	}

	paths := make([]string, 0, doc.Paths.Len())
	for p := range doc.Paths.Map() {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	var out []usecase.LoadOperation

	for _, p := range paths {
		item := doc.Paths.Find(p)
		ops := item.Operations()

		methods := make([]string, 0, len(ops))
		for m := range ops {
			methods = append(methods, m)
		}

		sort.Strings(methods)

		for _, m := range methods {
			out = append(out, loadOperation(p, m, item.Parameters, ops[m]))
		}
	}

	return out, nil
}

func loadOperation(path, method string, itemParams openapi3.Parameters, op *openapi3.Operation) usecase.LoadOperation {
	lo := new(usecase.LoadOperation)
	lo.ID = op.OperationID
	lo.Method = strings.ToUpper(method)
	lo.Path = path
	lo.Safe = lo.Method == "GET" || lo.Method == "HEAD"

	for _, p := range mergeParameters(itemParams, op.Parameters) {
		lp := usecase.LoadParam{Name: p.Name, Required: p.Required || p.In == "path", Values: paramValues(p)}

		switch p.In {
		case "path":
			lo.PathParams = append(lo.PathParams, lp)
		case "query":
			lo.QueryParams = append(lo.QueryParams, lp)
		case "header":
			lo.HeaderParams = append(lo.HeaderParams, lp)
		}
	}

	lo.BodyContentType, lo.Bodies = requestBodies(op)

	return *lo
}

// mergeParameters combines path-level and operation-level parameters; an
// operation parameter replaces a path-level one with the same name and place.
func mergeParameters(groups ...openapi3.Parameters) []*openapi3.Parameter {
	index := map[string]int{}

	var out []*openapi3.Parameter

	for _, group := range groups {
		for _, ref := range group {
			if ref == nil || ref.Value == nil {
				continue
			}

			key := ref.Value.In + " " + ref.Value.Name
			if i, ok := index[key]; ok {
				out[i] = ref.Value

				continue
			}

			index[key] = len(out)
			out = append(out, ref.Value)
		}
	}

	return out
}

// paramValues returns valid example values of a parameter: its example,
// default or enum, else values generated within the schema's bounds.
func paramValues(p *openapi3.Parameter) []string {
	if p.Example != nil {
		return []string{valueText(p.Example)}
	}

	if p.Schema == nil || p.Schema.Value == nil {
		return []string{defaultParam}
	}

	s := p.Schema.Value

	switch {
	case s.Example != nil:
		return []string{valueText(s.Example)}
	case len(s.Enum) > 0:
		out := make([]string, 0, variantCount)
		for _, v := range s.Enum[:min(len(s.Enum), variantCount)] {
			out = append(out, valueText(v))
		}

		return out
	case s.Default != nil:
		return []string{valueText(s.Default)}
	}

	return generatedValues(s)
}

// generatedValues draws values within the bounds of a scalar schema (arrays
// use their item schema: one query value is one item).
func generatedValues(s *openapi3.Schema) []string {
	switch {
	case s.Type.Is("array") && s.Items != nil && s.Items.Value != nil:
		return generatedValues(s.Items.Value)
	case s.Type.Is("integer"):
		return numberValues(s, true)
	case s.Type.Is("number"):
		return numberValues(s, false)
	case s.Type.Is("boolean"):
		return []string{"true", "false"}
	case s.Type.Is("string"):
		return []string{fitString(exampleString(s.Format), s)}
	default:
		return []string{defaultParam}
	}
}

// numberValues spreads values evenly over [min, max]; unbounded sides default
// to a window of 1000 from the other bound (or 1..1000).
func numberValues(s *openapi3.Schema, integer bool) []string {
	lo, hi := numberBounds(s, integer)

	seen := map[string]bool{}
	out := make([]string, 0, variantCount)

	for i := range variantCount {
		v := lo + (hi-lo)*float64(i)/float64(variantCount-1)
		if integer {
			v = math.Round(v)
		}

		text := strconv.FormatFloat(v, 'f', -1, 64)
		if !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}

	return out
}

// fitString pads or cuts a string to the schema's length bounds.
func fitString(v string, s *openapi3.Schema) string {
	for uint64(len(v)) < s.MinLength {
		v += "x"
	}

	if s.MaxLength != nil && uint64(len(v)) > *s.MaxLength {
		v = v[:*s.MaxLength]
	}

	return v
}

func valueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}

	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}

	return string(b)
}

// requestBodies picks the request media type (JSON first) and builds example
// bodies that the schema accepts. Non-JSON media types return no bodies.
func requestBodies(op *openapi3.Operation) (contentType string, bodies [][]byte) {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return "", nil
	}

	content := op.RequestBody.Value.Content
	types := make([]string, 0, len(content))

	for t := range content {
		types = append(types, t)
	}

	sort.Strings(types)

	idx := slices.IndexFunc(types, func(t string) bool { return t == jsonMIME || strings.HasSuffix(t, "+json") })
	if idx < 0 {
		if len(types) == 0 {
			return "", nil
		}

		return types[0], nil
	}

	mt := content[types[idx]]
	if mt == nil || mt.Schema == nil {
		return types[idx], nil
	}

	return types[idx], bodyVariants(mt)
}

// bodyVariants collects distinct example bodies that validate against the
// schema. When none does (examples ignore some constraints), the first
// generated one is kept so the operation still has a body.
func bodyVariants(mt *openapi3.MediaType) [][]byte {
	if mt.Example != nil {
		if b, err := json.Marshal(mt.Example); err == nil {
			return [][]byte{b}
		}
	}

	valid, first := drawBodyVariants(mt)

	if len(valid) > 0 {
		return valid
	}

	if first != nil {
		return [][]byte{first}
	}

	return nil
}

// drawBodyVariants draws random bodies and returns the distinct ones that
// validate against the schema, plus the first body drawn.
func drawBodyVariants(mt *openapi3.MediaType) (valid [][]byte, first []byte) {
	seen := map[string]bool{}

	for range bodyAttempts {
		doc := boundedExample(mt.Schema, 0)

		b, err := json.Marshal(doc)
		if err != nil || seen[string(b)] {
			continue
		}

		seen[string(b)] = true

		if first == nil {
			first = b
		}

		if acceptedBySchema(mt.Schema.Value, b) {
			valid = append(valid, b)
			if len(valid) == variantCount {
				break
			}
		}
	}

	return valid, first
}

// acceptedBySchema checks a body the way a server would see it: decoded from
// JSON, so numbers have the types the validator expects.
func acceptedBySchema(s *openapi3.Schema, body []byte) bool {
	if s == nil {
		return false
	}

	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return false
	}

	return s.VisitJSON(decoded) == nil
}

// maxExampleDepth stops recursion through self-referencing schemas.
const maxExampleDepth = 6

// boundedExample builds a random JSON value for a schema that respects its
// enum, numeric and length bounds, so most draws validate (the generic
// exampleFromSchema ignores bounds and is meant for mock responses).
func boundedExample(ref *openapi3.SchemaRef, depth int) any {
	if ref == nil || ref.Value == nil || depth > maxExampleDepth {
		return nil
	}

	s := ref.Value

	switch {
	case s.Example != nil:
		return s.Example
	case len(s.Enum) > 0:
		return s.Enum[rand.IntN(len(s.Enum))] //nolint:gosec // variant choice
	case s.Default != nil:
		return s.Default
	case len(s.AllOf) > 0:
		return mergedAllOf(s, depth)
	case len(s.OneOf) > 0:
		return boundedExample(s.OneOf[rand.IntN(len(s.OneOf))], depth+1) //nolint:gosec // variant choice
	case len(s.AnyOf) > 0:
		return boundedExample(s.AnyOf[0], depth+1)
	}

	return boundedByType(s, depth)
}

func boundedByType(s *openapi3.Schema, depth int) any {
	switch {
	case len(s.Properties) > 0 || s.Type.Is("object"):
		obj := map[string]any{}
		for name, prop := range s.Properties {
			obj[name] = boundedExample(prop, depth+1)
		}

		return obj
	case s.Type.Is("array"):
		n := max(int(s.MinItems), 1) //nolint:gosec // schema bound, small

		items := make([]any, 0, n)
		for range n {
			items = append(items, boundedExample(s.Items, depth+1))
		}

		return items
	default:
		return boundedScalar(s)
	}
}

// boundedScalar is an example value of a string, number or boolean schema.
func boundedScalar(s *openapi3.Schema) any {
	switch {
	case s.Type.Is("string"):
		return fitString(exampleString(s.Format), s)
	case s.Type.Is("integer"):
		return pickNumber(s, true)
	case s.Type.Is("number"):
		return pickNumber(s, false)
	case s.Type.Is("boolean"):
		return rand.IntN(coinSides) == 0 //nolint:gosec // variant choice
	default:
		return nil
	}
}

// mergedAllOf merges the objects of an allOf into one.
func mergedAllOf(s *openapi3.Schema, depth int) any {
	merged := map[string]any{}

	for _, part := range s.AllOf {
		if obj, ok := boundedExample(part, depth+1).(map[string]any); ok {
			for k, v := range obj {
				merged[k] = v
			}
		}
	}

	return merged
}

// pickNumber draws a number inside the schema's bounds.
func pickNumber(s *openapi3.Schema, integer bool) any {
	lo, hi := numberBounds(s, integer)

	v := lo + rand.Float64()*(hi-lo) //nolint:gosec // variant choice
	if integer {
		return int64(math.Round(math.Min(v, hi)))
	}

	return math.Round(v*100) / 100 //nolint:mnd // two decimals
}

// numberBounds is the range values of a numeric schema are drawn from.
// Unbounded sides default to a window of 1000 from the other bound (or
// 1..1000).
func numberBounds(s *openapi3.Schema, integer bool) (lo, hi float64) {
	lo, hi = 1.0, 1000.0

	switch {
	case s.Min != nil && s.Max != nil:
		lo, hi = *s.Min, *s.Max
	case s.Min != nil:
		lo, hi = *s.Min, *s.Min+hi
	case s.Max != nil:
		lo, hi = *s.Max-hi, *s.Max
	}

	if integer {
		lo, hi = math.Ceil(lo), math.Floor(hi)
	}

	return lo, max(lo, hi)
}
