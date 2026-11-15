package loadtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Worker tuning.
const (
	defaultMaxResponseBytes = 10 << 20
	defaultSlowDetail       = time.Second
	// detailBodyBytes is how much of a body a failure or slow sample keeps.
	detailBodyBytes = 4 << 10
	// detailBudget bounds how many failures and how many slow requests keep
	// their request and response, so a failure storm costs no memory or CPU.
	detailBudget   = 100
	statusClassDiv = 100
	httpClientErr  = 4
	httpServerErr  = 5
)

// WorkerConfig is what a Worker needs.
type WorkerConfig struct {
	Plan    *domain.Plan
	Client  *http.Client
	Builder *Builder
	// Sink receives every result; Collector.Record fits.
	Sink func(*Result)
	// Validator checks responses for steps with a contract check; may be nil.
	Validator usecase.ResponseValidator
	// SlowDetailAbove is the latency from which a request keeps its detail
	// (default 1 s; negative switches it off).
	SlowDetailAbove time.Duration
	// MaxResponseBytes caps how much of a response body is kept for checks
	// (default 10 MB); the rest is read and counted.
	MaxResponseBytes int64
}

// Worker runs scenario iterations: it builds each request, sends it, times it
// and turns the response into a Result.
type Worker struct {
	cfg       WorkerConfig
	open      bool
	exhausted atomic.Bool
	failLeft  atomic.Int64
	slowLeft  atomic.Int64
}

// NewWorker returns a worker for a validated plan.
func NewWorker(cfg *WorkerConfig) *Worker {
	w := new(Worker)
	w.cfg = *cfg
	w.open = cfg.Plan.Load.Kind == domain.LoadOpen

	if w.cfg.SlowDetailAbove == 0 {
		w.cfg.SlowDetailAbove = defaultSlowDetail
	}

	if w.cfg.MaxResponseBytes <= 0 {
		w.cfg.MaxResponseBytes = defaultMaxResponseBytes
	}

	w.failLeft.Store(detailBudget)
	w.slowLeft.Store(detailBudget)

	return w
}

// FeederExhausted reports whether a unique data feeder ran out of rows, which
// ends the scenarios that need it. The run should report it.
func (w *Worker) FeederExhausted() bool { return w.exhausted.Load() }

// Iteration returns the function the schedulers call for every iteration.
func (w *Worker) Iteration() Iteration { return w.run }

// run executes the steps of one scenario iteration in order.
func (w *Worker) run(ctx context.Context, info IterationInfo) {
	sc := &w.cfg.Plan.Scenarios[info.Scenario]
	state := NewIterState()

	for i := range sc.Steps {
		if ctx.Err() != nil {
			return
		}

		step := &sc.Steps[i]

		built, err := w.cfg.Builder.Build(ctx, sc.Name, step, state)
		if err != nil {
			w.buildFailed(sc.Name, step, info, err)

			return
		}

		// Only the first step of an open-model iteration is measured from the
		// intended start; later steps start when their predecessor is done.
		var origin time.Time
		if i == 0 && w.open {
			origin = info.Intended
		}

		res := w.send(ctx, built, step, state, origin)
		if res == nil {
			return
		}

		res.Scenario = sc.Name
		res.Warmup = info.Warmup
		w.cfg.Sink(res)

		last := i == len(sc.Steps)-1
		if !last && step.Think != nil && !sleepCtx(ctx, thinkDuration(step.Think)) {
			return
		}
	}
}

// buildFailed handles a step whose request cannot be built. A unique feeder
// running out only ends the scenario; anything else is reported as a failed
// check of the step, so it shows in the results instead of vanishing.
func (w *Worker) buildFailed(scenario string, step *domain.Step, info IterationInfo, err error) {
	if errors.Is(err, ErrFeederExhausted) {
		w.exhausted.Store(true)

		return
	}

	if errors.Is(err, context.Canceled) {
		return
	}

	res := new(Result)
	res.Scenario = scenario
	res.Step = scenario + "/" + stepName(step, nil)
	res.Operation = step.Method + " " + step.Path
	res.ChecksFailed = 1
	res.Warmup = info.Warmup
	res.Detail = new(Detail)
	res.Detail.Error = err.Error()
	w.cfg.Sink(res)
}

// send performs one request. origin is the intended start time for open-model
// latency; zero means "now". It returns nil when the run was cancelled.
func (w *Worker) send(ctx context.Context, b *Built, step *domain.Step, st *IterState, origin time.Time) *Result {
	tr := new(tracer)
	req := b.Request.WithContext(httptrace.WithClientTrace(ctx, tr.hooks()))
	sent := time.Now()

	if origin.IsZero() {
		origin = sent
	}

	res := new(Result)
	res.Step = b.Step
	res.Operation = b.Operation
	res.BytesOut = int64(len(b.Body))

	resp, err := w.cfg.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}

		res.Latency = time.Since(origin)
		res.ErrorClass = classify(err)
		res.Timings = tr.timings(sent, time.Now())
		w.attachDetail(res, b, nil, nil, err.Error())

		return res
	}

	body, total, readErr := readBody(resp.Body, w.cfg.MaxResponseBytes)
	_ = resp.Body.Close()
	end := time.Now()

	if readErr != nil && ctx.Err() != nil {
		return nil
	}

	res.Status = resp.StatusCode
	res.BytesIn = total
	res.Latency = end.Sub(origin)
	res.Timings = tr.timings(sent, end)

	if readErr != nil {
		res.ErrorClass = classify(readErr)
	}

	ctype := resp.Header.Get("Content-Type")
	env := new(checkEnv)
	env.res = res
	env.b = b
	env.body = body
	env.ctype = ctype

	w.runChecks(step, env)
	w.runExtracts(step, env, st)
	setStatusClass(res, env.statusExpected)
	w.keepDetail(env, resp)

	return res
}

// keepDetail attaches the request and response of failed and slow requests.
func (w *Worker) keepDetail(env *checkEnv, resp *http.Response) {
	res := env.res
	violations := env.violationText()

	failed := res.ErrorClass != "" || res.ChecksFailed > 0 || res.ContractViolations > 0
	slow := w.cfg.SlowDetailAbove > 0 && res.Latency >= w.cfg.SlowDetailAbove

	if failed || slow {
		w.attachDetail(res, env.b, resp, env.body, violations)
	}
}

// setStatusClass marks 4xx and 5xx answers as errors, unless a passing status
// check of the step expects that very status (a negative test).
func setStatusClass(res *Result, expected bool) {
	if res.ErrorClass != "" || expected {
		return
	}

	switch res.Status / statusClassDiv {
	case httpClientErr:
		res.ErrorClass = ClassHTTP4xx
	case httpServerErr:
		res.ErrorClass = ClassHTTP5xx
	}
}

// readBody reads up to limit bytes for checks and counts the rest.
func readBody(r io.Reader, limit int64) (body []byte, total int64, err error) {
	body, err = io.ReadAll(io.LimitReader(r, limit))
	total = int64(len(body))

	if err != nil {
		return body, total, fmt.Errorf("read response: %w", err)
	}

	rest, err := io.Copy(io.Discard, r)
	total += rest

	if err != nil {
		return body, total, fmt.Errorf("read response: %w", err)
	}

	return body, total, nil
}

// checkEnv is the response a step's checks look at.
type checkEnv struct {
	res   *Result
	b     *Built
	body  []byte
	ctype string

	parsedDone     bool
	parsed         any
	statusExpected bool
	violations     []usecase.ContractViolation
}

// json returns the decoded body once; ok is false when it is not JSON.
func (e *checkEnv) json() (any, bool) {
	if e.parsedDone {
		return e.parsed, e.parsed != nil
	}

	e.parsedDone = true

	trimmed := bytes.TrimSpace(e.body)
	if len(trimmed) == 0 {
		return nil, false
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()

	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}

	e.parsed = v

	return v, v != nil
}

func (e *checkEnv) violationText() string {
	parts := make([]string, 0, len(e.violations))

	for _, v := range e.violations {
		parts = append(parts, v.Path+": "+v.Message)
	}

	if len(parts) == 0 {
		return ""
	}

	return "contract: " + strings.Join(parts, "; ")
}

// runChecks evaluates the step's checks; failures are counted, not fatal.
func (w *Worker) runChecks(step *domain.Step, env *checkEnv) {
	for i := range step.Checks {
		c := &step.Checks[i]

		ok, evaluated := w.evalCheck(c, env)
		if !evaluated {
			continue
		}

		if ok {
			env.res.ChecksPassed++
		} else {
			env.res.ChecksFailed++
		}
	}
}

func (w *Worker) evalCheck(c *domain.Check, env *checkEnv) (ok, evaluated bool) {
	switch c.Kind {
	case domain.CheckStatus:
		ok = slices.Contains(c.Statuses, env.res.Status)
		if ok {
			env.statusExpected = true
		}

		return ok, true
	case domain.CheckLatency:
		return env.res.Latency <= c.MaxLatency.Std(), true
	case domain.CheckJSONPath:
		doc, isJSON := env.json()

		return isJSON && jsonPathCheck(doc, c), true
	case domain.CheckContract:
		return w.contractCheck(env)
	default:
		return false, true
	}
}

// contractCheck validates the response against the contract. evaluated is
// false when it was skipped (sampling, no validator, unknown operation).
func (w *Worker) contractCheck(env *checkEnv) (ok, evaluated bool) {
	if w.cfg.Validator == nil {
		return false, false
	}

	viol, checked := w.cfg.Validator.ValidateResponse(env.b.Method, env.b.Path, env.res.Status, env.ctype, env.body)
	if !checked {
		return false, false
	}

	env.violations = viol
	env.res.ContractViolations += len(viol)

	return len(viol) == 0, true
}

// jsonPathCheck tests the value at the check's path.
func jsonPathCheck(doc any, c *domain.Check) bool {
	got, found := domain.LookupJSONPath(doc, c.Path)

	switch c.Op {
	case domain.CheckExists:
		return found
	case domain.CheckEquals:
		return found && sameValue(got, c.Value)
	case domain.CheckContains:
		return found && containsValue(got, c.Value)
	default:
		return false
	}
}

// sameValue compares decoded JSON values by their text, so 5, 5.0 and the
// number 5 in the body all agree.
func sameValue(a, b any) bool { return domain.ValueText(a) == domain.ValueText(b) }

func containsValue(container, want any) bool {
	switch x := container.(type) {
	case string:
		return strings.Contains(x, domain.ValueText(want))
	case []any:
		return slices.ContainsFunc(x, func(e any) bool { return sameValue(e, want) })
	case map[string]any:
		_, ok := x[domain.ValueText(want)]

		return ok
	default:
		return false
	}
}

// runExtracts saves values of the response for later steps. A value that is
// not there counts as a failed check, because the next step will lack it.
func (w *Worker) runExtracts(step *domain.Step, env *checkEnv, st *IterState) {
	if len(step.Extracts) == 0 {
		return
	}

	doc, _ := env.json()

	for _, ex := range step.Extracts {
		v, found := domain.LookupJSONPath(doc, ex.Path)
		if !found {
			env.res.ChecksFailed++

			continue
		}

		st.Vars[ex.Var] = v
	}
}

// attachDetail keeps the request and response of a failure or a slow request,
// within the budgets.
func (w *Worker) attachDetail(res *Result, b *Built, resp *http.Response, body []byte, note string) {
	failed := res.ErrorClass != "" || res.ChecksFailed > 0 || res.ContractViolations > 0

	budget := &w.slowLeft
	if failed {
		budget = &w.failLeft
	}

	if budget.Add(-1) < 0 {
		return
	}

	d := new(Detail)
	d.Method = b.Method
	d.URL = b.Request.URL.String()
	d.RequestHeaders = usecase.RedactHeaders(b.Request.Header)
	d.RequestBody = clip(b.Body)
	d.Error = note

	if resp != nil {
		d.ResponseHeaders = usecase.RedactHeaders(resp.Header)
		d.ResponseBody = clip(body)
	}

	res.Detail = d
}

func clip(b []byte) string {
	if len(b) > detailBodyBytes {
		return string(b[:detailBodyBytes]) + "…"
	}

	return string(b)
}

// tracer records the phases of one request from httptrace callbacks, which
// may run on several goroutines.
type tracer struct {
	mu                  sync.Mutex
	dnsStart, dnsDone   time.Time
	connStart, connDone time.Time
	tlsStart, tlsDone   time.Time
	wrote, firstByte    time.Time
}

func (t *tracer) hooks() *httptrace.ClientTrace {
	stamp := func(dst *time.Time) {
		now := time.Now()

		t.mu.Lock()

		*dst = now

		t.mu.Unlock()
	}

	h := new(httptrace.ClientTrace)
	h.DNSStart = func(httptrace.DNSStartInfo) { stamp(&t.dnsStart) }
	h.DNSDone = func(httptrace.DNSDoneInfo) { stamp(&t.dnsDone) }
	h.ConnectStart = func(string, string) {
		now := time.Now()

		t.mu.Lock()

		if t.connStart.IsZero() {
			t.connStart = now
		}

		t.mu.Unlock()
	}
	h.ConnectDone = func(string, string, error) { stamp(&t.connDone) }
	h.TLSHandshakeStart = func() { stamp(&t.tlsStart) }
	h.TLSHandshakeDone = func(tls.ConnectionState, error) { stamp(&t.tlsDone) }
	h.WroteRequest = func(httptrace.WroteRequestInfo) { stamp(&t.wrote) }
	h.GotFirstResponseByte = func() { stamp(&t.firstByte) }

	return h
}

func span(from, to time.Time) time.Duration {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}

	return to.Sub(from)
}

// timings is the breakdown; sent is when the request started and end when its
// body was read. Phases that did not happen are zero.
func (t *tracer) timings(sent, end time.Time) Timings {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out Timings

	out.DNS = span(t.dnsStart, t.dnsDone)
	out.Connect = span(t.connStart, t.connDone)
	out.TLS = span(t.tlsStart, t.tlsDone)

	waitFrom := t.wrote
	if waitFrom.IsZero() {
		waitFrom = sent
	}

	out.TTFB = span(waitFrom, t.firstByte)
	out.Download = span(t.firstByte, end)

	return out
}

// classify maps a transport error to its class. Errors that are none of
// DNS, TLS, timeout or refused are connection resets and early closes.
func classify(err error) ErrorClass {
	var (
		dnsErr   *net.DNSError
		certErr  *tls.CertificateVerificationError
		recErr   tls.RecordHeaderError
		alertErr tls.AlertError
		netErr   net.Error
	)

	msg := err.Error()

	switch {
	case errors.As(err, &dnsErr):
		return ClassDNS
	case errors.As(err, &certErr), errors.As(err, &recErr), errors.As(err, &alertErr),
		strings.Contains(msg, "tls:"), strings.Contains(msg, "x509:"):
		return ClassTLS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return ClassTimeout
	case errors.Is(err, syscall.ECONNREFUSED), strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "actively refused"):
		return ClassRefused
	default:
		return ClassReset
	}
}

// sleepCtx waits d or until ctx ends; it reports false when ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// thinkDuration draws one pause from the step's think-time distribution. A
// lognormal pause is parameterised by the mean and standard deviation of the
// pause itself, not of its logarithm.
func thinkDuration(t *domain.Think) time.Duration {
	switch t.Kind {
	case domain.ThinkConstant:
		return t.Duration.Std()
	case domain.ThinkUniform:
		lo, hi := t.Min.Std(), t.Max.Std()

		return lo + time.Duration(rand.Float64()*float64(hi-lo)) //nolint:gosec // load shaping
	case domain.ThinkNormal:
		d := float64(t.Mean) + float64(t.StdDev)*rand.NormFloat64() //nolint:gosec // load shaping

		return time.Duration(max(d, 0))
	case domain.ThinkLognormal:
		mean, sd := float64(t.Mean), float64(t.StdDev)
		sigma2 := math.Log(1 + (sd/mean)*(sd/mean))
		mu := math.Log(mean) - sigma2/2 //nolint:mnd // lognormal mean correction

		return time.Duration(math.Exp(mu + math.Sqrt(sigma2)*rand.NormFloat64())) //nolint:gosec // load shaping
	default:
		return 0
	}
}
