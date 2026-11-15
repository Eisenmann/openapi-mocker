package loadtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Errors of the request builder.
var (
	ErrUnknownOperation = errors.New("step refers to an operation that is not in the contract")
	ErrNoFeeder         = errors.New("template refers to a data feeder that the plan does not define")
	ErrNoSchemaBody     = errors.New("{{faker.fromSchema}} needs a step with a contract operation that has a request body")
	ErrNoSecret         = errors.New("the secret named by target.auth.ref is not set")
	ErrBadRequest       = errors.New("request cannot be built")
)

const (
	userAgent       = "openapi-mocker-load"
	headerContent   = "Content-Type"
	headerAuth      = "Authorization"
	contentTypeJSON = "application/json"
)

// BuilderConfig is what a Builder needs.
type BuilderConfig struct {
	// Target is the resolved base URL; request paths are appended to its path.
	Target *url.URL
	// ProjectID scopes {{counter}} values.
	ProjectID string
	// Engine renders {{templates}}.
	Engine *usecase.TemplateEngine
	// Operations are the contract's operations, for steps that name one.
	Operations []usecase.LoadOperation
	// Feeders are the plan's data feeders by name.
	Feeders map[string]*Feeder
	// Auth is added to every request; its secret is read through Secrets.
	Auth *domain.AuthRef
	// Secrets returns the secret a ref names (for example an environment
	// variable); empty means not set.
	Secrets func(ref string) string
}

// Builder turns plan steps into HTTP requests. It is safe for concurrent
// use; the per-iteration state lives in IterState.
type Builder struct {
	base    *url.URL
	project string
	engine  *usecase.TemplateEngine
	ops     map[string]*usecase.LoadOperation
	feeders map[string]*Feeder
	auth    *domain.AuthRef
	secret  string
}

// NewBuilder checks the configuration and prepares the operation index.
func NewBuilder(cfg *BuilderConfig) (*Builder, error) {
	b := new(Builder)
	b.base = cfg.Target
	b.project = cfg.ProjectID
	b.engine = cfg.Engine
	b.feeders = cfg.Feeders
	b.auth = cfg.Auth
	b.ops = make(map[string]*usecase.LoadOperation, len(cfg.Operations)*2) //nolint:mnd // id and ref keys

	for i := range cfg.Operations {
		op := &cfg.Operations[i]
		b.ops[op.Ref()] = op

		if op.ID != "" {
			b.ops[op.ID] = op
		}
	}

	if b.auth != nil {
		if cfg.Secrets != nil {
			b.secret = cfg.Secrets(b.auth.Ref)
		}

		if b.secret == "" {
			return nil, fmt.Errorf("%w (%s)", ErrNoSecret, b.auth.Ref)
		}
	}

	return b, nil
}

// IterState is the data of one iteration: variables saved by extracts and
// the feeder rows the iteration drew (one per feeder, kept for all steps).
type IterState struct {
	Vars map[string]any
	rows map[string]map[string]any
}

// NewIterState returns the state for a new iteration.
func NewIterState() *IterState {
	return &IterState{Vars: map[string]any{}, rows: map[string]map[string]any{}}
}

// Built is a request ready to send with what the worker needs to report it.
type Built struct {
	Request *http.Request
	Body    []byte
	Method  string
	// Path is the concrete request path without the query, as the contract
	// matches it.
	Path string
	// Operation names the request in reports: the contract operation, or the
	// method and path as written in the plan.
	Operation string
	// Step is "<scenario>/<step>".
	Step string
	// Spec is the contract operation, nil for hand-written steps.
	Spec *usecase.LoadOperation
}

func lookupOperation(ops map[string]*usecase.LoadOperation, ref string) (*usecase.LoadOperation, bool) {
	if op, ok := ops[ref]; ok {
		return op, true
	}

	fields := strings.Fields(ref)
	if len(fields) == 2 { //nolint:mnd // "METHOD /path"
		op, ok := ops[strings.ToUpper(fields[0])+" "+fields[1]]

		return op, ok
	}

	return nil, false
}

func stepName(step *domain.Step, spec *usecase.LoadOperation) string {
	switch {
	case step.Name != "":
		return step.Name
	case spec != nil:
		return spec.Name()
	default:
		return step.Method + " " + step.Path
	}
}

// Build makes the request of one step.
func (b *Builder) Build(ctx context.Context, scenario string, step *domain.Step, st *IterState) (*Built, error) {
	var spec *usecase.LoadOperation

	if step.Operation != "" {
		op, ok := lookupOperation(b.ops, step.Operation)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownOperation, step.Operation)
		}

		spec = op
	}

	out := new(Built)
	out.Spec = spec
	out.Step = scenario + "/" + stepName(step, spec)
	out.Method = step.Method
	tmpl := step.Path

	if spec != nil {
		out.Operation = spec.Name()

		if out.Method == "" {
			out.Method = spec.Method
		}

		if tmpl == "" {
			tmpl = spec.Path
		}
	} else {
		out.Operation = step.Method + " " + step.Path
	}

	rc := new(renderCtx)
	rc.b = b
	rc.st = st
	rc.spec = spec

	err := rc.assemble(ctx, step, out, tmpl)
	if err != nil {
		return nil, err
	}

	return out, nil
}

// renderCtx renders the templates of one request and remembers the first
// problem a placeholder ran into.
type renderCtx struct {
	b    *Builder
	st   *IterState
	spec *usecase.LoadOperation
	tc   usecase.TemplateContext
	err  error
}

func (r *renderCtx) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *renderCtx) text(s string) string {
	t := r.tc
	t.JSON = false

	return r.b.engine.Render(s, &t)
}

func (r *renderCtx) jsonText(s string) string {
	t := r.tc
	t.JSON = true

	return r.b.engine.Render(s, &t)
}

// resolve answers the load tester's own placeholders; the template engine
// answers the rest ({{faker.*}}, {{uuid}}, {{now}}, {{counter}}).
func (r *renderCtx) resolve(head string, _ []string) (any, bool) {
	ns, rest, _ := strings.Cut(head, ".")

	switch ns {
	case "vars":
		v, _ := domain.LookupJSONPath(r.st.Vars, rest)

		return v, true
	case "data":
		return r.dataValue(rest), true
	case "faker":
		if rest == "fromSchema" {
			return r.fromSchema(), true
		}
	}

	return nil, false
}

func (r *renderCtx) dataValue(rest string) any {
	feeder, col, _ := strings.Cut(rest, ".")

	row, ok := r.st.rows[feeder]
	if !ok {
		f := r.b.feeders[feeder]
		if f == nil {
			r.fail(fmt.Errorf("%w: %q", ErrNoFeeder, feeder))

			return nil
		}

		row, ok = f.Next()
		if !ok {
			r.fail(fmt.Errorf("%w: %q", ErrFeederExhausted, feeder))

			return nil
		}

		r.st.rows[feeder] = row
	}

	v, _ := domain.LookupJSONPath(row, col)

	return v
}

func (r *renderCtx) fromSchema() any {
	if r.spec == nil || len(r.spec.Bodies) == 0 {
		r.fail(ErrNoSchemaBody)

		return nil
	}

	var v any

	dec := json.NewDecoder(bytes.NewReader(r.spec.Bodies[rand.IntN(len(r.spec.Bodies))])) //nolint:gosec // variant choice
	dec.UseNumber()

	_ = dec.Decode(&v)

	return v
}

// assemble fills the request into out.
func (r *renderCtx) assemble(ctx context.Context, step *domain.Step, out *Built, tmpl string) error {
	r.tc.ProjectID = r.b.project
	r.tc.Method = out.Method
	r.tc.Resolve = r.resolve

	rendered := r.text(tmpl)
	pathPart, rawQuery, _ := strings.Cut(rendered, "?")
	pathPart = r.fillPathParams(pathPart)
	r.tc.Path = pathPart
	out.Path = pathPart

	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return fmt.Errorf("%w: query %q: %w", ErrBadRequest, rawQuery, err)
	}

	r.addRequiredParams(query)

	headers := r.headers(step)
	body, ctype := r.body(step)
	out.Body = body

	if r.err != nil {
		return r.err
	}

	u := *r.b.base
	// pathPart is already escaped; keep that form in RawPath and the decoded
	// form in Path so it is not escaped twice.
	joined := strings.TrimRight(r.b.base.EscapedPath(), "/") + "/" + strings.TrimLeft(pathPart, "/")

	u.RawPath = joined

	decoded, derr := url.PathUnescape(joined)
	if derr != nil {
		decoded = joined
	}

	u.Path = decoded
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, out.Method, u.String(), bodyReader(body))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	req.Header = headers

	if len(body) > 0 && req.Header.Get(headerContent) == "" {
		req.Header.Set(headerContent, ctype)
	}

	r.b.applyAuth(req)
	out.Request = req

	return nil
}

func bodyReader(body []byte) io.Reader {
	if len(body) == 0 {
		return nil
	}

	return bytes.NewReader(body)
}

// fillPathParams replaces {param} of the contract operation with a valid
// example value.
func (r *renderCtx) fillPathParams(p string) string {
	if r.spec == nil {
		return p
	}

	for i := range r.spec.PathParams {
		pp := &r.spec.PathParams[i]
		if len(pp.Values) == 0 {
			continue
		}

		v := pp.Values[rand.IntN(len(pp.Values))] //nolint:gosec // example choice
		p = strings.ReplaceAll(p, "{"+pp.Name+"}", url.PathEscape(v))
	}

	return p
}

// addRequiredParams adds the operation's required query parameters unless the
// step wrote them itself.
func (r *renderCtx) addRequiredParams(q url.Values) {
	if r.spec == nil {
		return
	}

	for i := range r.spec.QueryParams {
		qp := &r.spec.QueryParams[i]
		if qp.Required && len(qp.Values) > 0 && !q.Has(qp.Name) {
			q.Set(qp.Name, qp.Values[rand.IntN(len(qp.Values))]) //nolint:gosec // example choice
		}
	}
}

func (r *renderCtx) headers(step *domain.Step) http.Header {
	h := make(http.Header)
	h.Set("User-Agent", userAgent)

	if r.spec != nil {
		for i := range r.spec.HeaderParams {
			hp := &r.spec.HeaderParams[i]
			if hp.Required && len(hp.Values) > 0 {
				h.Set(hp.Name, hp.Values[rand.IntN(len(hp.Values))]) //nolint:gosec // example choice
			}
		}
	}

	for k, v := range step.Headers {
		h.Set(k, r.text(v))
	}

	return h
}

// body returns the request body and its media type: the step's own body
// (rendered), or a valid example body of the contract operation.
func (r *renderCtx) body(step *domain.Step) (body []byte, contentType string) {
	if step.Body != "" {
		trimmed := strings.TrimSpace(step.Body)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return []byte(r.jsonText(step.Body)), contentTypeJSON
		}

		return []byte(r.text(step.Body)), "text/plain"
	}

	if r.spec == nil || len(r.spec.Bodies) == 0 {
		return nil, ""
	}

	ctype := r.spec.BodyContentType
	if ctype == "" {
		ctype = contentTypeJSON
	}

	return r.spec.Bodies[rand.IntN(len(r.spec.Bodies))], ctype //nolint:gosec // example choice
}

func (b *Builder) applyAuth(req *http.Request) {
	if b.auth == nil {
		return
	}

	switch b.auth.Type {
	case domain.AuthBearer:
		if req.Header.Get(headerAuth) == "" {
			req.Header.Set(headerAuth, "Bearer "+b.secret)
		}
	case domain.AuthBasic:
		if req.Header.Get(headerAuth) == "" {
			req.Header.Set(headerAuth, "Basic "+base64.StdEncoding.EncodeToString([]byte(b.secret)))
		}
	case domain.AuthHeader:
		if req.Header.Get(b.auth.Header) == "" {
			req.Header.Set(b.auth.Header, b.secret)
		}
	}
}
