package usecase

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// ErrPlanSyntax is returned when the input is neither valid YAML nor JSON.
var ErrPlanSyntax = errors.New("load plan is not valid YAML or JSON")

// PlanError is a problem in a load plan, with the path of the field it is
// about, for example "scenarios.buy.steps[2].checks[0]".
type PlanError struct {
	Path string
	Msg  string
}

func (e *PlanError) Error() string {
	if e.Path == "" {
		return e.Msg
	}

	return e.Path + ": " + e.Msg
}

// ParsePlan reads a load plan from YAML or JSON (the same document structure
// in both) and validates it. Problems are reported as *PlanError with the
// path of the field; scenarios and data feeders are keyed by name and come
// out sorted by name, so a plan always runs the same way.
//
//	name: pets smoke
//	target: {baseUrl: "mock://p1"}
//	scenarios:
//	  browse:
//	    weight: 8
//	    steps:
//	      - operation: listPets
//	        checks: ["status in [200]", "latency < 800ms", contract]
//	load:
//	  model: open
//	  stages: [{duration: 1m, rate: 10}, {duration: 5m, rate: 10}]
//	thresholds: ["http.latency.p95 < 500ms", "errors.rate < 1%"]
func ParsePlan(data []byte) (*domain.Plan, error) {
	tree, err := decodeTree(data)
	if err != nil {
		return nil, err
	}

	plan, err := decodePlan(tree)
	if err != nil {
		return nil, err
	}

	err = plan.Validate()
	if err != nil {
		return nil, fmt.Errorf("load plan is invalid: %w", err)
	}

	return plan, nil
}

// decodeTree parses JSON (when the text starts with { or [) or YAML into a
// generic tree.
func decodeTree(data []byte) (any, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: the document is empty", ErrPlanSyntax)
	}

	var tree any

	if trimmed[0] == '{' || trimmed[0] == '[' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()

		err := dec.Decode(&tree)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPlanSyntax, err)
		}

		return tree, nil
	}

	err := yaml.Unmarshal(trimmed, &tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlanSyntax, err)
	}

	return tree, nil
}

// decoder walks the generic tree; the first problem is kept and decoding
// goes on with zero values, so callers need no error handling per field.
type decoder struct {
	err error
}

func (d *decoder) fail(p, msg string) {
	if d.err == nil {
		d.err = &PlanError{Path: p, Msg: msg}
	}
}

func (d *decoder) failErr(p string, err error) {
	if d.err == nil {
		d.err = &PlanError{Path: p, Msg: err.Error()}
	}
}

func join(parent, key string) string {
	if parent == "" {
		return key
	}

	return parent + "." + key
}

func index(parent string, i int) string { return parent + "[" + strconv.Itoa(i) + "]" }

// obj is one decoded mapping.
type obj struct {
	d    *decoder
	path string
	m    map[string]any
	seen map[string]bool
}

// object turns v into a mapping; a missing or wrong-typed value gives an empty
// object (and an error when v is present but not a mapping).
func (d *decoder) object(p string, v any) *obj {
	o := &obj{d: d, path: p, m: map[string]any{}, seen: map[string]bool{}}

	switch x := v.(type) {
	case nil:
	case map[string]any:
		o.m = x
	case map[any]any:
		for k, e := range x {
			o.m[fmt.Sprint(k)] = e
		}
	default:
		d.fail(p, "must be a mapping")
	}

	return o
}

func (o *obj) raw(key string) (any, bool) {
	o.seen[key] = true
	v, ok := o.m[key]

	return v, ok && v != nil
}

// done reports keys nobody asked for, which are almost always typos.
func (o *obj) done() {
	keys := make([]string, 0, len(o.m))

	for k := range o.m {
		if !o.seen[k] {
			keys = append(keys, k)
		}
	}

	sort.Strings(keys)

	if len(keys) > 0 {
		o.d.fail(join(o.path, keys[0]), "unknown field")
	}
}

func (o *obj) child(key string) *obj {
	v, _ := o.raw(key)

	return o.d.object(join(o.path, key), v)
}

func (o *obj) str(key string) string {
	v, ok := o.raw(key)
	if !ok {
		return ""
	}

	s, isStr := v.(string)
	if !isStr {
		o.d.fail(join(o.path, key), "must be a string")
	}

	return s
}

func (o *obj) boolean(key string) bool {
	v, ok := o.raw(key)
	if !ok {
		return false
	}

	b, isBool := v.(bool)
	if !isBool {
		o.d.fail(join(o.path, key), "must be true or false")
	}

	return b
}

// integerValue reads a whole number from YAML or JSON numbers or from a
// numeric string ("500").
func integerValue(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case uint64:
		return int(min(x, math.MaxInt32)), true //nolint:gosec // clamped
	case float64:
		return int(x), x == math.Trunc(x)
	case json.Number:
		n, err := strconv.Atoi(x.String())

		return n, err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))

		return n, err == nil
	default:
		return 0, false
	}
}

func (o *obj) integer(key string) int {
	v, ok := o.raw(key)
	if !ok {
		return 0
	}

	n, isInt := integerValue(v)
	if !isInt {
		o.d.fail(join(o.path, key), "must be a whole number")
	}

	return n
}

func (o *obj) duration(key string) time.Duration {
	v, ok := o.raw(key)
	if !ok {
		return 0
	}

	return o.d.durationOf(join(o.path, key), v)
}

func (d *decoder) durationOf(p string, v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		d.fail(p, "must be a duration with a unit, such as 30s, 2m or 300ms")

		return 0
	}

	dur, err := parseDuration(s)
	if err != nil {
		d.failErr(p, err)
	}

	return dur
}

func (o *obj) strList(key string) []string {
	v, ok := o.raw(key)
	if !ok {
		return nil
	}

	list, isList := v.([]any)
	if !isList {
		o.d.fail(join(o.path, key), "must be a list")

		return nil
	}

	out := make([]string, 0, len(list))

	for i, e := range list {
		s, isStr := e.(string)
		if !isStr {
			o.d.fail(index(join(o.path, key), i), "must be a string")

			continue
		}

		out = append(out, s)
	}

	return out
}

// list returns the elements of a list field with their paths.
func (o *obj) list(key string) (items []any, base string) {
	v, ok := o.raw(key)
	base = join(o.path, key)

	if !ok {
		return nil, base
	}

	l, isList := v.([]any)
	if !isList {
		o.d.fail(base, "must be a list")

		return nil, base
	}

	return l, base
}

// names returns the keys of a mapping field in sorted order.
func (o *obj) names(key string) (sorted []string, m map[string]any) {
	v, ok := o.raw(key)
	if !ok {
		return nil, nil
	}

	inner := o.d.object(join(o.path, key), v)
	for k := range inner.m {
		sorted = append(sorted, k)
	}

	sort.Strings(sorted)

	return sorted, inner.m
}

func decodePlan(tree any) (*domain.Plan, error) {
	d := new(decoder)
	root := d.object("", tree)
	p := new(domain.Plan)

	p.Name = root.str("name")
	p.Description = root.str("description")
	decodeTarget(root.child("target"), &p.Target)
	p.Data = decodeFeeders(root)
	p.Scenarios = decodeScenarios(root)
	decodeLoad(root.child("load"), p)
	p.Thresholds = decodeThresholds(root)
	p.Outputs = decodeOutputs(root)
	root.done()

	if d.err != nil {
		return nil, d.err
	}

	return p, nil
}

func decodeTarget(o *obj, t *domain.Target) {
	t.BaseURL = o.str("baseUrl")
	t.AllowHosts = o.strList("allowHosts")

	h := o.child("http")
	t.HTTP.Protocol = domain.Protocol(h.str("protocol"))
	t.HTTP.Timeout = domain.Duration(h.duration("timeout"))
	t.HTTP.DialTimeout = domain.Duration(h.duration("dialTimeout"))
	t.HTTP.TLSHandshakeTimeout = domain.Duration(h.duration("tlsHandshakeTimeout"))
	t.HTTP.IdleConnTimeout = domain.Duration(h.duration("idleConnTimeout"))
	t.HTTP.DisableKeepAlives = h.boolean("disableKeepAlives")
	t.HTTP.MaxConnsPerHost = h.integer("maxConnsPerHost")
	t.HTTP.MaxIdleConnsPerHost = h.integer("maxIdleConnsPerHost")
	t.HTTP.InsecureSkipVerify = h.boolean("insecureSkipVerify")
	h.done()

	if _, ok := o.raw("auth"); ok {
		a := o.child("auth")
		t.Auth = &domain.AuthRef{
			Type:   domain.AuthKind(a.str("type")),
			Ref:    a.str("ref"),
			Header: a.str("header"),
		}

		a.done()
	}

	o.done()
}

func decodeFeeders(root *obj) []domain.DataFeeder {
	names, m := root.names("data")
	out := make([]domain.DataFeeder, 0, len(names))

	for _, name := range names {
		f := root.d.object(join(join(root.path, "data"), name), m[name])

		var feeder domain.DataFeeder

		feeder.Name = name
		feeder.File = f.str("file")
		feeder.Format = domain.FeederFormat(f.str("format"))
		feeder.Mode = domain.FeederMode(f.str("mode"))

		if feeder.Format == "" {
			feeder.Format = feederFormatOf(feeder.File)
		}

		f.done()

		out = append(out, feeder)
	}

	return out
}

// feederFormatOf guesses the format from the file extension.
func feederFormatOf(file string) domain.FeederFormat {
	switch strings.ToLower(path.Ext(file)) {
	case ".csv":
		return domain.FeederCSV
	case ".json":
		return domain.FeederJSON
	default:
		return ""
	}
}

func decodeScenarios(root *obj) []domain.Scenario {
	names, m := root.names("scenarios")
	out := make([]domain.Scenario, 0, len(names))

	for _, name := range names {
		p := join(join(root.path, "scenarios"), name)
		o := root.d.object(p, m[name])

		var sc domain.Scenario

		sc.Name = name
		sc.Weight = 1

		if _, ok := o.raw("weight"); ok {
			sc.Weight = o.integer("weight")
		}

		items, base := o.list("steps")
		for i, it := range items {
			sc.Steps = append(sc.Steps, decodeStep(root.d, index(base, i), it))
		}

		o.done()

		out = append(out, sc)
	}

	return out
}

func decodeStep(d *decoder, p string, v any) domain.Step {
	if s, ok := v.(string); ok {
		return stepFromShorthand(s)
	}

	o := d.object(p, v)

	var st domain.Step

	st.Name = o.str("name")
	st.Operation = o.str("operation")
	st.Method = strings.ToUpper(o.str("method"))
	st.Path = o.str("path")
	st.Headers = decodeHeaders(o)
	st.Body = decodeBody(o)
	st.Checks = decodeChecks(o)
	st.Extracts = decodeExtracts(o)
	st.Think = decodeThink(o)
	o.done()

	return st
}

func decodeHeaders(o *obj) map[string]string {
	v, ok := o.raw("headers")
	if !ok {
		return nil
	}

	h := o.d.object(join(o.path, "headers"), v)
	out := make(map[string]string, len(h.m))

	for k, e := range h.m {
		s, isStr := e.(string)
		if !isStr {
			o.d.fail(join(join(o.path, "headers"), k), "must be a string")

			continue
		}

		out[k] = s
	}

	h.seen = nil

	return out
}

// decodeBody returns the body as text; a mapping or list is written as JSON.
func decodeBody(o *obj) string {
	v, ok := o.raw("body")
	if !ok {
		return ""
	}

	if s, isStr := v.(string); isStr {
		return s
	}

	b, err := json.Marshal(v)
	if err != nil {
		o.d.fail(join(o.path, "body"), "cannot be written as JSON")

		return ""
	}

	return string(b)
}

func decodeChecks(o *obj) []domain.Check {
	items, base := o.list("checks")
	out := make([]domain.Check, 0, len(items))

	for i, it := range items {
		p := index(base, i)

		expr, ok := it.(string)
		if !ok {
			o.d.fail(p, "must be a check expression such as \"status in [200]\"")

			continue
		}

		c, err := parseCheckExpr(expr)
		if err != nil {
			o.d.failErr(p, err)

			continue
		}

		out = append(out, c)
	}

	return out
}

func decodeExtracts(o *obj) []domain.Extract {
	v, ok := o.raw("extract")
	if !ok {
		return nil
	}

	m := o.d.object(join(o.path, "extract"), v)
	names := make([]string, 0, len(m.m))

	for k := range m.m {
		names = append(names, k)
	}

	sort.Strings(names)

	out := make([]domain.Extract, 0, len(names))

	for _, name := range names {
		expr, isStr := m.m[name].(string)
		if !isStr || !strings.HasPrefix(expr, "$") || jsonPathOf(expr) == "" {
			o.d.fail(join(join(o.path, "extract"), name), "must be a JSON path such as $.id")

			continue
		}

		out = append(out, domain.Extract{Var: name, Path: jsonPathOf(expr)})
	}

	m.seen = nil

	return out
}

// decodeThink reads "1s" (constant) or a mapping with kind and its numbers.
func decodeThink(o *obj) *domain.Think {
	v, ok := o.raw("think")
	if !ok {
		return nil
	}

	p := join(o.path, "think")
	t := new(domain.Think)

	if _, isStr := v.(string); isStr {
		t.Kind = domain.ThinkConstant
		t.Duration = domain.Duration(o.d.durationOf(p, v))

		return t
	}

	m := o.d.object(p, v)
	t.Kind = domain.ThinkKind(m.str("kind"))
	t.Duration = domain.Duration(m.duration("duration"))
	t.Min = domain.Duration(m.duration("min"))
	t.Max = domain.Duration(m.duration("max"))
	t.Mean = domain.Duration(m.duration("mean"))
	t.StdDev = domain.Duration(m.duration("stdDev"))
	m.done()

	return t
}

func decodeLoad(o *obj, p *domain.Plan) {
	p.Load.Kind = domain.LoadModelKind(o.str("model"))
	p.Load.Iterations = o.integer("iterations")
	p.Load.VUs = o.integer("vus")
	p.Load.PerVU = o.boolean("perVu")
	p.Warmup = domain.Duration(o.duration("warmup"))
	p.GracefulStop = domain.Duration(o.duration("gracefulStop"))
	p.MaxInFlight = o.integer("maxInFlight")
	p.ContractSampleEvery = o.integer("contractSampleEvery")

	items, base := o.list("stages")
	for i, it := range items {
		s := o.d.object(index(base, i), it)

		var st domain.Stage

		st.Duration = domain.Duration(s.duration("duration"))
		st.Rate = s.integer("rate")
		st.VUs = s.integer("vus")
		s.done()

		p.Load.Stages = append(p.Load.Stages, st)
	}

	o.done()
}

func decodeThresholds(root *obj) []domain.Threshold {
	items, base := root.list("thresholds")
	out := make([]domain.Threshold, 0, len(items))

	for i, it := range items {
		out = append(out, decodeThreshold(root.d, index(base, i), it))
	}

	return out
}

// decodeThreshold reads "metric < value" or a mapping with expr (or metric,
// op, value, scope) plus abortOnFail and abortDelay.
func decodeThreshold(d *decoder, p string, v any) domain.Threshold {
	var t domain.Threshold

	if s, ok := v.(string); ok {
		parsed, err := parseThresholdExpr(s)
		if err != nil {
			d.failErr(p, err)
		}

		return parsed
	}

	o := d.object(p, v)

	if expr := o.str("expr"); expr != "" {
		parsed, err := parseThresholdExpr(expr)
		if err != nil {
			d.failErr(join(p, "expr"), err)
		}

		t = parsed
	} else {
		t.Metric = o.str("metric")
		t.Op = domain.ThresholdOp(o.str("op"))
		t.Scope = o.str("scope")
		t.Value = thresholdNumber(o, t.Metric)
	}

	t.AbortOnFail = o.boolean("abortOnFail")
	t.AbortDelay = domain.Duration(o.duration("abortDelay"))
	o.done()

	return t
}

// thresholdNumber reads the value field of a threshold mapping, which may be
// a number or text such as "500ms" or "1%".
func thresholdNumber(o *obj, metric string) float64 {
	v, ok := o.raw("value")
	if !ok {
		return 0
	}

	var text string

	switch x := v.(type) {
	case string:
		text = x
	case int, int64, float64, json.Number:
		text = fmt.Sprint(x)
	default:
		o.d.fail(join(o.path, "value"), "must be a number or text such as 500ms or 1%")

		return 0
	}

	f, err := parseThresholdValue(metric, text)
	if err != nil {
		o.d.failErr(join(o.path, "value"), err)
	}

	return f
}

func decodeOutputs(root *obj) []domain.Output {
	list := root.strList("outputs")
	out := make([]domain.Output, 0, len(list))

	for _, f := range list {
		out = append(out, domain.Output{Format: domain.OutputFormat(f)})
	}

	return out
}
