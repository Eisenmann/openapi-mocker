// Package engine runs a load plan: it wires the HTTP client, request builder,
// worker, scheduler, metrics collector and threshold checks together and turns
// the result into report files. It implements usecase.LoadEngine.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Artifact names written for every run.
const (
	ArtifactSummary    = "summary.json"
	ArtifactHistograms = "histograms.bin"
	ArtifactSeries     = "timeseries.csv"
	ArtifactSamples    = "samples.json"

	defaultGraceful  = 10 * time.Second
	monitorInterval  = time.Second
	percentP50       = 50
	percentP95       = 95
	percentP99       = 99
	usPerMs          = 1000.0
	seedMixConstant  = 0x9e3779b97f4a7c15
	jsonIndent       = "  "
	abortReasonShort = "%s %s %g failed (observed %g)"
)

// Errors of the engine.
var (
	ErrNoContract   = errors.New("the plan needs the project's contract, but it could not be read")
	ErrNoFeederFS   = errors.New("the plan uses data files, but no data directory is available")
	ErrNoValidator  = errors.New("contract checks are used, but no response validator is available")
	ErrUnknownLoad  = errors.New("unknown load model")
	ErrNoOperations = errors.New("the contract could not be read for operation lookup")
)

// Config is what the engine needs from the outside.
type Config struct {
	// Operations reads contract operations (the OpenAPI adapter).
	Operations usecase.LoadOperationLister
	// Contract returns the raw contract of a project; it may be nil when only
	// hand-written steps without contract checks are run.
	Contract func(ctx context.Context, projectID string) ([]byte, error)
	// NewValidator builds the response validator from a contract; sampleEvery
	// is the plan's contractSampleEvery.
	NewValidator func(raw []byte, sampleEvery int) (usecase.ResponseValidator, error)
	// Data is the directory plan data files are read from; may be nil.
	Data fs.FS
	// Secrets returns the secret a target.auth.ref names.
	Secrets func(ref string) string
	// Templates renders {{...}} in steps (default: a new engine).
	Templates *usecase.TemplateEngine
	// Version is recorded in the report's environment.
	Version string
	// Now may be replaced in tests.
	Now func() time.Time
}

// Engine runs plans.
type Engine struct {
	cfg Config
}

var _ usecase.LoadEngine = (*Engine)(nil)

// New returns an engine.
func New(cfg *Config) *Engine {
	e := new(Engine)
	e.cfg = *cfg

	if e.cfg.Templates == nil {
		e.cfg.Templates = usecase.NewTemplateEngine()
	}

	if e.cfg.Now == nil {
		e.cfg.Now = time.Now
	}

	return e
}

// run is everything one execution owns.
type run struct {
	in        *usecase.LoadExecution
	plan      *domain.Plan
	client    *loadtest.Client
	collector *loadtest.Collector
	stats     *loadtest.Stats
	worker    *loadtest.Worker
	started   time.Time

	abortReason string
}

// Execute runs the plan until its load model ends, ctx is cancelled or a
// threshold with abortOnFail fails. Cancelling still returns what was
// measured.
func (e *Engine) Execute(ctx context.Context, in *usecase.LoadExecution) (*usecase.LoadOutcome, error) {
	r, err := e.prepare(ctx, in)
	if err != nil {
		return nil, err
	}

	defer r.client.Close()

	r.started = e.cfg.Now()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	collectorDone := make(chan struct{})
	collectorCtx, stopCollector := context.WithCancel(context.WithoutCancel(ctx))

	go func() {
		defer close(collectorDone)

		r.collector.Run(collectorCtx)
	}()

	monitorDone := make(chan struct{})

	go func() {
		defer close(monitorDone)

		r.monitor(runCtx, cancel)
	}()

	schedErr := r.schedule(runCtx)

	cancel()
	<-monitorDone
	stopCollector()
	<-collectorDone

	if schedErr != nil {
		return nil, schedErr
	}

	return e.outcome(ctx, r)
}

// prepare builds the pieces of a run.
func (e *Engine) prepare(ctx context.Context, in *usecase.LoadExecution) (*run, error) {
	plan := in.Plan

	ops, validator, err := e.contract(ctx, in)
	if err != nil {
		return nil, err
	}

	feeders, err := e.feeders(plan)
	if err != nil {
		return nil, err
	}

	bc := new(loadtest.BuilderConfig)
	bc.Target = in.Target.BaseURL
	bc.ProjectID = in.ProjectID
	bc.Engine = e.cfg.Templates
	bc.Operations = ops
	bc.Feeders = feeders
	bc.Auth = plan.Target.Auth
	bc.Secrets = e.cfg.Secrets

	builder, err := loadtest.NewBuilder(bc)
	if err != nil {
		return nil, fmt.Errorf("prepare requests: %w", err)
	}

	r := new(run)
	r.in = in
	r.plan = plan
	r.stats = new(loadtest.Stats)

	opts := plan.Target.HTTP
	if opts.MaxConnsPerHost == 0 && in.Target.MaxConns > 0 {
		opts.MaxConnsPerHost = in.Target.MaxConns
	}

	r.client = loadtest.NewClientWith(&opts, in.Target.BlockPrivate)

	co := new(loadtest.CollectorOptions)
	co.Warmup = plan.Warmup.Std()
	co.Stats = r.stats
	r.collector = loadtest.NewCollector(co)

	wc := new(loadtest.WorkerConfig)
	wc.Plan = plan
	wc.Client = r.client.HTTP
	wc.Builder = builder
	wc.Sink = r.collector.Record
	wc.Validator = validator
	r.worker = loadtest.NewWorker(wc)

	return r, nil
}

// contract reads the operations and builds the validator, when the plan needs
// them.
//
//nolint:ireturn // the validator is chosen by the caller
func (e *Engine) contract(
	ctx context.Context, in *usecase.LoadExecution,
) ([]usecase.LoadOperation, usecase.ResponseValidator, error) {
	needOps, needContract := planNeeds(in.Plan)
	if !needOps && !needContract {
		return nil, nil, nil
	}

	if e.cfg.Contract == nil || e.cfg.Operations == nil {
		return nil, nil, ErrNoContract
	}

	raw, err := e.cfg.Contract(ctx, in.ProjectID)
	if err != nil || len(raw) == 0 {
		return nil, nil, fmt.Errorf("%w: %w", ErrNoContract, err)
	}

	ops, err := e.cfg.Operations.LoadOperations(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrNoOperations, err)
	}

	if !needContract {
		return ops, nil, nil
	}

	v, err := e.validator(raw, in.Plan.ContractSampleEvery)
	if err != nil {
		return nil, nil, err
	}

	return ops, v, nil
}

//nolint:ireturn // the validator is chosen by the caller
func (e *Engine) validator(raw []byte, sampleEvery int) (usecase.ResponseValidator, error) {
	if e.cfg.NewValidator == nil {
		return nil, ErrNoValidator
	}

	v, err := e.cfg.NewValidator(raw, sampleEvery)
	if err != nil {
		return nil, fmt.Errorf("prepare contract checks: %w", err)
	}

	return v, nil
}

// planNeeds says whether the plan refers to contract operations and whether it
// has contract checks.
func planNeeds(plan *domain.Plan) (ops, contract bool) {
	for i := range plan.Scenarios {
		for j := range plan.Scenarios[i].Steps {
			st := &plan.Scenarios[i].Steps[j]
			if st.Operation != "" {
				ops = true
			}

			for k := range st.Checks {
				if st.Checks[k].Kind == domain.CheckContract {
					contract = true
				}
			}
		}
	}

	return ops, contract
}

func (e *Engine) feeders(plan *domain.Plan) (map[string]*loadtest.Feeder, error) {
	if len(plan.Data) == 0 {
		return nil, nil //nolint:nilnil // no feeders is a valid state
	}

	if e.cfg.Data == nil {
		return nil, ErrNoFeederFS
	}

	f, err := loadtest.LoadFeeders(e.cfg.Data, plan.Data)
	if err != nil {
		return nil, fmt.Errorf("load data files: %w", err)
	}

	return f, nil
}

// schedule runs the plan's load model.
func (r *run) schedule(ctx context.Context) error {
	plan := r.plan
	weights := make([]int, len(plan.Scenarios))

	for i := range plan.Scenarios {
		weights[i] = plan.Scenarios[i].Weight
	}

	graceful := plan.GracefulStop.Std()
	if graceful <= 0 {
		graceful = defaultGraceful
	}

	seed := uint64(r.started.UnixNano()) * seedMixConstant //nolint:gosec // mixing a timestamp

	var err error

	switch plan.Load.Kind {
	case domain.LoadOpen:
		c := new(loadtest.OpenConfig)
		c.Stages, c.Weights, c.Warmup, c.GracefulStop = plan.Load.Stages, weights, plan.Warmup.Std(), graceful
		c.MaxInFlight, c.Seed, c.Run, c.Stats = plan.MaxInFlight, seed, r.worker.Iteration(), r.stats
		err = loadtest.RunOpen(ctx, c)
	case domain.LoadClosed:
		c := new(loadtest.ClosedConfig)
		c.Stages, c.Weights, c.Warmup, c.GracefulStop = plan.Load.Stages, weights, plan.Warmup.Std(), graceful
		c.Seed, c.Run, c.Stats = seed, r.worker.Iteration(), r.stats
		err = loadtest.RunClosed(ctx, c)
	case domain.LoadIterations:
		c := new(loadtest.IterationsConfig)
		c.VUs, c.Iterations, c.PerVU = plan.Load.VUs, plan.Load.Iterations, plan.Load.PerVU
		c.Weights, c.Warmup, c.GracefulStop = weights, plan.Warmup.Std(), graceful
		c.Seed, c.Run, c.Stats = seed, r.worker.Iteration(), r.stats
		err = loadtest.RunIterations(ctx, c)
	default:
		return ErrUnknownLoad
	}

	if err != nil {
		return fmt.Errorf("run load: %w", err)
	}

	return nil
}

// monitor reports progress and stops the run when a threshold with abortOnFail
// fails. It ends when ctx does.
func (r *run) monitor(ctx context.Context, stop context.CancelFunc) {
	t := time.NewTicker(monitorInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		elapsed := time.Since(r.started)

		if r.in.Progress != nil {
			r.in.Progress(r.progress(elapsed))
		}

		measured := max(elapsed-r.plan.Warmup.Std(), 0)

		if res := loadtest.CheckAbort(r.collector, r.plan.Thresholds, measured); res != nil {
			t := &res.Threshold
			r.abortReason = fmt.Sprintf(abortReasonShort, t.Metric, t.Op, t.Value, res.Observed)

			stop()

			return
		}
	}
}

func (r *run) progress(elapsed time.Duration) usecase.LoadProgress {
	var p usecase.LoadProgress

	snap := r.collector.Snapshot()

	p.Elapsed = elapsed
	p.Requests = snap.Overall.Totals.Requests
	p.Errors = snap.Overall.Totals.Errors
	p.Dropped = r.stats.Dropped()
	p.InFlight = r.stats.InFlight()
	p.VUs = r.stats.ActiveVUs()
	p.P95Ms = float64(snap.Overall.Latency.ValueAtPercentile(percentP95)) / usPerMs

	if secs := snap.MeasuredSeconds; secs > 0 {
		p.RPS = float64(p.Requests) / float64(secs)
	}

	return p
}

// outcome evaluates the thresholds and builds the report files.
func (e *Engine) outcome(ctx context.Context, r *run) (*usecase.LoadOutcome, error) {
	snap := r.collector.Snapshot()
	results := loadtest.Evaluate(r.collector, r.plan.Thresholds)

	out := new(usecase.LoadOutcome)
	out.AbortReason = r.abortReason
	out.Thresholds = domainResults(results)
	out.Totals = totalsOf(snap)

	aborted := ctx.Err() != nil || r.abortReason != ""
	finished := e.cfg.Now()

	rec := new(domain.LoadRun)
	rec.ID = r.in.RunID
	rec.ProjectID = r.in.ProjectID
	rec.Plan = *r.plan
	rec.Target = r.in.Target.BaseURL.String()
	rec.Actor = r.in.Actor
	rec.StartedAt = &r.started
	rec.FinishedAt = &finished
	rec.Thresholds = out.Thresholds
	rec.State = domain.LoadRunFinished
	rec.AbortReason = r.abortReason

	if aborted {
		rec.State = domain.LoadRunAborted
	}

	rec.Verdict = domain.VerdictPass
	if aborted || !loadtest.Passed(results) {
		rec.Verdict = domain.VerdictFail
	}

	rep := report.Build(&report.Input{Run: rec, Summary: snap, Environment: report.CurrentEnvironment(e.cfg.Version)})

	var err error

	out.Artifacts, err = artifacts(rep, snap)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func domainResults(in []loadtest.ThresholdResult) []domain.LoadThresholdResult {
	out := make([]domain.LoadThresholdResult, 0, len(in))

	for i := range in {
		var d domain.LoadThresholdResult

		d.Threshold = in[i].Threshold
		d.Observed = in[i].Observed
		d.HasData = in[i].HasData
		d.Passed = in[i].Passed
		out = append(out, d)
	}

	return out
}

func totalsOf(s *loadtest.Summary) *domain.LoadTotals {
	t := new(domain.LoadTotals)
	o := &s.Overall.Totals

	t.Requests = o.Requests
	t.Errors = o.Errors
	t.ContractViolations = o.ContractViolations
	t.Dropped = s.Dropped
	t.Untrustworthy = s.Untrustworthy
	t.UntrustworthyReason = s.UntrustworthyReason

	if o.Requests > 0 {
		t.ErrorRate = float64(o.Errors) / float64(o.Requests)
	}

	if s.MeasuredSeconds > 0 {
		t.ThroughputRPS = float64(o.Requests) / float64(s.MeasuredSeconds)
	}

	t.LatencyP50Ms = float64(s.Overall.Latency.ValueAtPercentile(percentP50)) / usPerMs
	t.LatencyP95Ms = float64(s.Overall.Latency.ValueAtPercentile(percentP95)) / usPerMs
	t.LatencyP99Ms = float64(s.Overall.Latency.ValueAtPercentile(percentP99)) / usPerMs

	if math.IsNaN(t.ErrorRate) {
		t.ErrorRate = 0
	}

	return t
}

// artifacts renders the files stored with the run.
func artifacts(rep *report.Report, snap *loadtest.Summary) (map[string][]byte, error) {
	out := make(map[string][]byte, 4) //nolint:mnd // four artifact files

	summary, err := report.JSON(rep)
	if err != nil {
		return nil, fmt.Errorf("render report: %w", err)
	}

	out[ArtifactSummary] = summary

	series, err := report.CSV(rep)
	if err != nil {
		return nil, fmt.Errorf("render time series: %w", err)
	}

	out[ArtifactSeries] = series

	both := map[string]any{"slowest": rep.Slowest, "firstFailed": rep.FirstFailed}

	samples, err := json.MarshalIndent(both, "", jsonIndent)
	if err != nil {
		return nil, fmt.Errorf("render samples: %w", err)
	}

	out[ArtifactSamples] = samples

	hist, err := EncodeHistograms(histogramSet(snap))
	if err != nil {
		return nil, err
	}

	out[ArtifactHistograms] = hist

	return out, nil
}
