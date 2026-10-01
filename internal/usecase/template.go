package usecase

import (
	"bytes"
	"encoding/json"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TemplateEngine renders {{placeholders}} in mock responses, so one mock can
// react to the request instead of needing a rule per case.
//
// Placeholders:
//
//	{{request.method}} {{request.path}}
//	{{request.query.name}} {{request.header.X-Name}}
//	{{request.body}} {{request.body.user.name}} {{request.body.items[0].id}}
//	{{path.id}}                         path parameter of the matched route
//	{{uuid}}
//	{{now}} {{now +2h}} {{now -3d}}     RFC 3339 time, optionally shifted
//	{{now.unix}} {{timestamp}} {{date}} Unix seconds / YYYY-MM-DD
//	{{counter}} {{counter orders}}      per-project counter, +1 on every use
//	{{faker.name}} {{faker.firstName}} {{faker.lastName}} {{faker.username}}
//	{{faker.email}} {{faker.phone}} {{faker.city}} {{faker.country}}
//	{{faker.company}} {{faker.word}} {{faker.sentence}} {{faker.sentence 10}}
//	{{faker.int}} {{faker.int 1 100}} {{faker.float 0 1}} {{faker.bool}}
//	{{faker.pick red green blue}}
//
// In a JSON body a placeholder inside a string is escaped for JSON, and one
// outside a string is replaced by its JSON value (strings get quotes, a
// missing request field becomes null). Unknown placeholders are left as-is,
// so existing mocks keep working. Counters live in memory only.
type TemplateEngine struct {
	mu       sync.Mutex
	counters map[string]int64
	now      func() time.Time
}

// NewTemplateEngine returns an engine with all counters at zero.
func NewTemplateEngine() *TemplateEngine {
	return &TemplateEngine{mu: sync.Mutex{}, counters: map[string]int64{}, now: time.Now}
}

// TemplateContext is the request a template is rendered for.
type TemplateContext struct {
	ProjectID  string
	Method     string
	Path       string
	PathParams map[string]string
	Query      map[string][]string
	Header     map[string][]string
	// Body is the raw request body (for MCP: the tool call arguments).
	Body []byte
	// JSON is true when the rendered text is a JSON document.
	JSON bool

	body   any
	parsed bool
}

const (
	tagOpen, tagClose = "{{", "}}"
	nsNow             = "now"
)

// Render replaces the placeholders in src.
func (t *TemplateEngine) Render(src string, ctx *TemplateContext) string {
	if !strings.Contains(src, tagOpen) {
		return src
	}

	var out strings.Builder

	inString, escaped := false, false

	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], tagOpen) {
			end := strings.Index(src[i+len(tagOpen):], tagClose)
			if end >= 0 {
				tag := src[i+len(tagOpen) : i+len(tagOpen)+end]
				if v, ok := t.value(tag, ctx); ok {
					out.WriteString(emit(v, ctx.JSON, inString))

					i += len(tagOpen) + end + len(tagClose)

					continue
				}
			}
		}

		c := src[i]
		if ctx.JSON {
			inString, escaped = trackJSONString(c, inString, escaped)
		}

		out.WriteByte(c)

		i++
	}

	return out.String()
}

// trackJSONString follows whether the scanner is inside a JSON string literal.
func trackJSONString(c byte, inString, escaped bool) (nowInString, nowEscaped bool) {
	switch {
	case escaped:
		return inString, false
	case inString && c == '\\':
		return true, true
	case c == '"':
		return !inString, false
	default:
		return inString, false
	}
}

// RenderValue renders the placeholders in every string of a decoded JSON
// value. A string that is exactly one placeholder is replaced by the typed
// value (a number stays a number); other strings are rendered as text.
func (t *TemplateEngine) RenderValue(v any, ctx *TemplateContext) any {
	switch x := v.(type) {
	case string:
		return t.renderString(x, ctx)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = t.RenderValue(x[i], ctx)
		}

		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = t.RenderValue(e, ctx)
		}

		return out
	default:
		return v
	}
}

func (t *TemplateEngine) renderString(s string, ctx *TemplateContext) any {
	if !strings.Contains(s, tagOpen) {
		return s
	}

	if strings.HasPrefix(s, tagOpen) && strings.HasSuffix(s, tagClose) &&
		strings.Count(s, tagOpen) == 1 {
		v, ok := t.value(s[len(tagOpen):len(s)-len(tagClose)], ctx)
		if ok {
			return v
		}
	}

	text := *ctx
	text.JSON = false

	return t.Render(s, &text)
}

// emit formats a placeholder value for the surrounding text.
func emit(v any, jsonDoc, inString bool) string {
	switch {
	case jsonDoc && inString:
		b := encodeJSON(textOf(v))

		return string(b[1 : len(b)-1])
	case jsonDoc:
		return string(encodeJSON(v))
	default:
		return textOf(v)
	}
}

// encodeJSON marshals v without escaping <, > and &.
func encodeJSON(v any) []byte {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	//nolint:errchkjson // v is decoded JSON or a primitive; it always encodes.
	if enc.Encode(v) != nil {
		return []byte("null")
	}

	return bytes.TrimRight(buf.Bytes(), "\n")
}

// textOf renders a value as plain text (objects and arrays as JSON).
func textOf(v any) string {
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
		return string(encodeJSON(v))
	}
}

// value evaluates one placeholder. ok is false when the tag is not a known
// placeholder (it is then left in the output unchanged).
func (t *TemplateEngine) value(tag string, ctx *TemplateContext) (v any, ok bool) {
	fields := strings.Fields(tag)
	if len(fields) == 0 {
		return nil, false
	}

	head, args := fields[0], fields[1:]
	for i := range args {
		args[i] = strings.Trim(args[i], `'"`)
	}

	ns, rest, _ := strings.Cut(head, ".")

	switch ns {
	case "request":
		return requestValue(rest, ctx)
	case "path":
		return pathValue(rest, ctx)
	case "faker":
		return fakerValue(rest, args)
	case "uuid":
		return newUUID(), rest == ""
	case nsNow, "timestamp", "date":
		return t.timeValue(ns, rest, args)
	case "counter":
		return t.counter(ctx.ProjectID, args), rest == ""
	default:
		return nil, false
	}
}

func pathValue(name string, ctx *TemplateContext) (any, bool) {
	if name == "" {
		return nil, false
	}

	v, found := ctx.PathParams[name]
	if !found {
		return nil, true
	}

	return typed(v), true
}

var numberRe = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?$`)

// typed turns text that is a plain number ("7", "-1.5") into a JSON number, so
// {"id": {{path.id}}} yields 7; anything else stays a string.
func typed(s string) any {
	if numberRe.MatchString(s) {
		return json.Number(s)
	}

	return s
}

func requestValue(rest string, ctx *TemplateContext) (any, bool) {
	kind, name, _ := strings.Cut(rest, ".")

	switch kind {
	case "method":
		return ctx.Method, name == ""
	case "path":
		return ctx.Path, name == ""
	case "query":
		return firstValue(ctx.Query, name, false), name != ""
	case "header":
		return firstValue(ctx.Header, name, true), name != ""
	case "body":
		return ctx.bodyValue(name), true
	default:
		return nil, false
	}
}

// firstValue returns the first value of a query parameter or header (nil when
// absent).
func firstValue(m map[string][]string, name string, header bool) any {
	if header {
		name = textproto.CanonicalMIMEHeaderKey(name)
	}

	if vals := m[name]; len(vals) > 0 {
		return typed(vals[0])
	}

	return nil
}

// bodyValue returns the request body, or the value at a dotted path
// ("user.name", "items[0].id") inside it. Missing values are nil.
func (c *TemplateContext) bodyValue(path string) any {
	c.parseBody()

	if path == "" {
		if c.body == nil && len(c.Body) > 0 {
			return string(c.Body)
		}

		return c.body
	}

	cur := c.body
	for _, seg := range strings.FieldsFunc(path, func(r rune) bool { return r == '.' || r == '[' || r == ']' }) {
		cur = step(cur, seg)
	}

	return cur
}

// parseBody decodes the request body once (leaving nil when it is not JSON).
func (c *TemplateContext) parseBody() {
	if c.parsed {
		return
	}

	c.parsed = true

	dec := json.NewDecoder(bytes.NewReader(c.Body))
	dec.UseNumber()

	if dec.Decode(&c.body) != nil {
		c.body = nil
	}
}

// step descends one path segment into a decoded JSON value (nil when absent).
func step(cur any, seg string) any {
	switch x := cur.(type) {
	case map[string]any:
		return x[seg]
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(x) {
			return nil
		}

		return x[i]
	default:
		return nil
	}
}

func (t *TemplateEngine) counter(projectID string, args []string) int64 {
	name := "default"
	if len(args) > 0 {
		name = args[0]
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	key := projectID + "|" + name
	t.counters[key]++

	return t.counters[key]
}

func (t *TemplateEngine) timeValue(ns, rest string, args []string) (any, bool) {
	now := t.now().UTC()

	if len(args) > 0 {
		d, ok := parseOffset(args[0])
		if !ok {
			return nil, false
		}

		now = now.Add(d)
	}

	return formatTime(ns+"."+rest, now)
}

// formatTime renders now for the placeholder kind ("now.", "now.unix", ...).
func formatTime(kind string, now time.Time) (any, bool) {
	switch kind {
	case "timestamp.", "now.unix":
		return now.Unix(), true
	case "date.":
		return now.Format("2006-01-02"), true
	case "now.":
		return now.Format(time.RFC3339), true
	default:
		return nil, false
	}
}

// parseOffset parses "+2h", "-30m" or "+3d" (days).
func parseOffset(s string) (time.Duration, bool) {
	if s == "" || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}

	sign := time.Duration(1)
	if s[0] == '-' {
		sign = -1
	}

	s = s[1:]

	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, false
		}

		return sign * time.Duration(n) * 24 * time.Hour, true
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}

	return sign * d, true
}
