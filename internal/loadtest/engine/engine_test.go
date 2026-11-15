package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/engine"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func target(t *testing.T, srv *httptest.Server) *usecase.LoadTarget {
	t.Helper()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	lt := new(usecase.LoadTarget)
	lt.BaseURL = u

	return lt
}

func openPlan(rate int, dur time.Duration) *domain.Plan {
	p := new(domain.Plan)
	p.Name = "t"
	p.Target.BaseURL = "http://x"
	p.Scenarios = []domain.Scenario{{Name: "s", Weight: 1, Steps: []domain.Step{
		{Method: "GET", Path: "/ok", Checks: []domain.Check{{Kind: domain.CheckStatus, Statuses: []int{200}}}},
	}}}
	p.Load = domain.LoadModel{Kind: domain.LoadOpen, Stages: []domain.Stage{{Duration: domain.Duration(dur), Rate: rate}}}
	p.GracefulStop = domain.Duration(time.Second)

	return p
}

func exec(ctx context.Context, e *engine.Engine, plan *domain.Plan, tg *usecase.LoadTarget) (*usecase.LoadOutcome, error) {
	in := new(usecase.LoadExecution)
	in.RunID = "run_1"
	in.ProjectID = "p1"
	in.Plan = plan
	in.Target = tg

	return e.Execute(ctx, in)
}

func TestEngine_OpenModelRunProducesReportAndVerdict(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	plan := openPlan(100, 2*time.Second)
	plan.Thresholds = []domain.Threshold{
		{Metric: "errors.rate", Op: domain.OpLess, Value: 0.01},
		{Metric: "http.latency.p95", Op: domain.OpLess, Value: 500},
		{Metric: "throughput.rps", Op: domain.OpGreater, Value: 1e6},
	}

	out, err := exec(context.Background(), engine.New(new(engine.Config)), plan, target(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	if hits.Load() < 90 || hits.Load() > 110 {
		t.Errorf("a 2 s ramp from 0 to 100/s should send about 100, sent %d requests", hits.Load())
	}

	if len(out.Thresholds) != 3 || !out.Thresholds[0].Passed || !out.Thresholds[1].Passed || out.Thresholds[2].Passed {
		t.Errorf("thresholds: %+v", out.Thresholds)
	}

	if out.Totals.Requests != hits.Load() || out.Totals.Errors != 0 || out.Totals.LatencyP95Ms < 0 {
		t.Errorf("totals: %+v", out.Totals)
	}

	for _, name := range []string{engine.ArtifactSummary, engine.ArtifactHistograms, engine.ArtifactSeries, engine.ArtifactSamples} {
		if len(out.Artifacts[name]) == 0 {
			t.Errorf("artifact %s is missing", name)
		}
	}

	rep, err := report.Parse(out.Artifacts[engine.ArtifactSummary])
	if err != nil {
		t.Fatal(err)
	}

	if rep.Run.State != "finished" || rep.Run.Verdict != "fail" || rep.Totals.Requests != hits.Load() ||
		len(rep.Series) < 2 || rep.Environment.CPUs == 0 {
		t.Errorf("report: %+v", rep.Run)
	}

	hists, err := engine.DecodeHistograms(out.Artifacts[engine.ArtifactHistograms])
	if err != nil || hists["overall"] == nil || hists["overall"].Count() != hits.Load() {
		t.Errorf("histograms: %v %v", hists, err)
	}
}

func TestEngine_ThresholdAbortStopsEarly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()

	plan := openPlan(50, time.Minute)
	plan.Thresholds = []domain.Threshold{{Metric: "errors.rate", Op: domain.OpLess, Value: 0.1, AbortOnFail: true}}

	start := time.Now()

	out, err := exec(context.Background(), engine.New(new(engine.Config)), plan, target(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	if time.Since(start) > 15*time.Second {
		t.Errorf("an abortOnFail threshold must end the run early, took %v", time.Since(start))
	}

	if out.AbortReason == "" || out.Thresholds[0].Passed {
		t.Errorf("outcome: %+v", out)
	}

	rep, _ := report.Parse(out.Artifacts[engine.ArtifactSummary])
	if rep.Run.State != "aborted" || rep.Run.Verdict != "fail" {
		t.Errorf("report run: %+v", rep.Run)
	}
}

func TestEngine_CancelKeepsPartialResults(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	out, err := exec(ctx, engine.New(new(engine.Config)), openPlan(50, time.Minute), target(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	if out.Totals.Requests == 0 {
		t.Error("requests sent before the cancel must be kept")
	}
}

func TestEngine_ClosedAndIterationModels(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	plan := openPlan(0, 0)
	plan.Load = domain.LoadModel{Kind: domain.LoadIterations, VUs: 3, Iterations: 30}

	out, err := exec(context.Background(), engine.New(new(engine.Config)), plan, target(t, srv))
	if err != nil || out.Totals.Requests != 30 {
		t.Fatalf("iterations: %v %+v", err, out)
	}

	plan.Load = domain.LoadModel{Kind: domain.LoadClosed, Stages: []domain.Stage{{Duration: domain.Duration(time.Second), VUs: 2}}}

	hits.Store(0)

	if _, err := exec(context.Background(), engine.New(new(engine.Config)), plan, target(t, srv)); err != nil || hits.Load() == 0 {
		t.Errorf("closed: %v hits %d", err, hits.Load())
	}
}

func TestEngine_DataFilesAndProgress(t *testing.T) {
	t.Parallel()

	var last atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.Store(r.URL.Path)
	}))
	defer srv.Close()

	plan := openPlan(0, 0)
	plan.Load = domain.LoadModel{Kind: domain.LoadIterations, VUs: 1, Iterations: 1}
	plan.Data = []domain.DataFeeder{{Name: "u", File: "u.csv", Format: domain.FeederCSV, Mode: domain.FeederSequential}}
	plan.Scenarios[0].Steps[0].Path = "/user/{{data.u.name}}"

	if _, err := exec(context.Background(), engine.New(new(engine.Config)), plan, target(t, srv)); err == nil {
		t.Error("data files without a data directory must fail")
	}

	cfg := new(engine.Config)
	cfg.Data = fstest.MapFS{"u.csv": {Data: []byte("name\nann\n")}}

	if _, err := exec(context.Background(), engine.New(cfg), plan, target(t, srv)); err != nil {
		t.Fatal(err)
	}

	if last.Load() != "/user/ann" {
		t.Errorf("path = %v", last.Load())
	}
}

func TestEngine_NeedsContractForOperations(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	plan := openPlan(10, time.Second)
	plan.Scenarios[0].Steps[0] = domain.Step{Operation: "listPets"}

	if _, err := exec(context.Background(), engine.New(new(engine.Config)), plan, target(t, srv)); err == nil {
		t.Error("an operation step without a contract source must fail before any load is sent")
	}
}

func TestHistograms_DecodeRejectsGarbage(t *testing.T) {
	t.Parallel()

	for _, in := range [][]byte{nil, {0xff}, {1, 5, 'a'}} {
		if _, err := engine.DecodeHistograms(in); err == nil {
			t.Errorf("%v must fail", in)
		}
	}
}
