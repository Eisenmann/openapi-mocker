package loadtest_test

import (
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
)

type fakeSource map[string]float64

func (f fakeSource) Metric(metric, scope string) (float64, bool) {
	v, ok := f[metric+"|"+scope]

	return v, ok
}

func th(metric string, op domain.ThresholdOp, v float64) domain.Threshold {
	return domain.Threshold{Metric: metric, Op: op, Value: v}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	cases := []struct {
		op   domain.ThresholdOp
		a, b float64
		want bool
	}{
		{domain.OpLess, 1, 2, true},
		{domain.OpLess, 2, 2, false},
		{domain.OpLessEqual, 2, 2, true},
		{domain.OpLessEqual, 3, 2, false},
		{domain.OpGreater, 3, 2, true},
		{domain.OpGreater, 2, 2, false},
		{domain.OpGreaterEqual, 2, 2, true},
		{domain.OpGreaterEqual, 1, 2, false},
		{domain.OpEqual, 2, 2, true},
		{domain.OpEqual, 1, 2, false},
		{domain.OpNotEqual, 1, 2, true},
		{domain.OpNotEqual, 2, 2, false},
		{"~", 1, 1, false},
	}

	for _, c := range cases {
		if got := loadtest.Compare(c.a, c.op, c.b); got != c.want {
			t.Errorf("%v %s %v = %v", c.a, c.op, c.b, got)
		}
	}
}

func TestEvaluate_EveryMetric(t *testing.T) {
	t.Parallel()

	src := fakeSource{
		"http.latency.p95|":         420,
		"http.latency.p99|":         900,
		"http.latency.avg|":         120,
		"http.latency.max|":         2500,
		"errors.rate|":              0.004,
		"errors.count|":             12,
		"checks.rate|":              0.999,
		"contract.violations|":      0,
		"throughput.rps|":           98,
		"dropped.count|":            3,
		"http.latency.p95|listPets": 700,
	}

	scoped := th("http.latency.p95", domain.OpLess, 500)
	scoped.Scope = "listPets"

	results := loadtest.Evaluate(src, []domain.Threshold{
		th("http.latency.p95", domain.OpLess, 500),       // pass
		th("http.latency.p99", domain.OpLess, 800),       // fail
		th("http.latency.avg", domain.OpLessEqual, 120),  // pass
		th("http.latency.max", domain.OpLess, 3000),      // pass
		th("errors.rate", domain.OpLess, 0.01),           // pass
		th("errors.count", domain.OpEqual, 0),            // fail
		th("checks.rate", domain.OpGreater, 0.99),        // pass
		th("contract.violations", domain.OpEqual, 0),     // pass
		th("throughput.rps", domain.OpGreaterEqual, 100), // fail
		th("dropped.count", domain.OpEqual, 0),           // fail
		scoped,                                           // fail: scope p95 is 700
		th("http.latency.p50", domain.OpLess, 1),         // fail: no data
	})

	wantPass := []bool{true, false, true, true, true, false, true, true, false, false, false, false}
	for i, r := range results {
		if r.Passed != wantPass[i] {
			t.Errorf("threshold %d (%s %s): passed=%v observed=%v hasData=%v", i, r.Threshold.Metric, r.Threshold.Op,
				r.Passed, r.Observed, r.HasData)
		}
	}

	if results[11].HasData || !results[0].HasData || results[10].Observed != 700 {
		t.Error("HasData and scope are not carried through")
	}

	if loadtest.Passed(results) || !loadtest.Passed(results[:1]) || !loadtest.Passed(nil) {
		t.Error("Passed must require every threshold")
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	ok := loadtest.Evaluate(fakeSource{"errors.rate|": 0}, []domain.Threshold{th("errors.rate", domain.OpLess, 1)})
	bad := loadtest.Evaluate(fakeSource{"errors.rate|": 2}, []domain.Threshold{th("errors.rate", domain.OpLess, 1)})

	if loadtest.ExitCode(ok, false) != loadtest.ExitPass || loadtest.ExitCode(nil, false) != loadtest.ExitPass {
		t.Error("passing thresholds exit 0")
	}

	if loadtest.ExitCode(bad, false) != loadtest.ExitThresholdFailed || loadtest.ExitCode(ok, true) != loadtest.ExitThresholdFailed {
		t.Error("a failed threshold or an abort exits 1")
	}

	if loadtest.ExitPlanError != 2 {
		t.Error("plan errors exit 2")
	}
}

func TestCheckAbort_Timing(t *testing.T) {
	t.Parallel()

	failing := th("errors.rate", domain.OpLess, 0.01)
	failing.AbortOnFail = true
	failing.AbortDelay = domain.Duration(10 * time.Second)

	noAbort := th("http.latency.p99", domain.OpLess, 1) // fails but may not abort
	noData := th("http.latency.p50", domain.OpLess, 1)
	noData.AbortOnFail = true

	src := fakeSource{"errors.rate|": 0.5, "http.latency.p99|": 999}
	all := []domain.Threshold{noAbort, noData, failing}

	if got := loadtest.CheckAbort(src, all, 9*time.Second); got != nil {
		t.Errorf("must wait for the abort delay, got %+v", got)
	}

	got := loadtest.CheckAbort(src, all, 10*time.Second)
	if got == nil || got.Threshold.Metric != "errors.rate" || got.Observed != 0.5 {
		t.Fatalf("got %+v at the delay", got)
	}

	if loadtest.CheckAbort(fakeSource{"errors.rate|": 0.001}, all, time.Minute) != nil {
		t.Error("a passing threshold never aborts")
	}

	if loadtest.CheckAbort(fakeSource{}, all, time.Minute) != nil {
		t.Error("a threshold without data does not abort yet")
	}
}

// The collector plugs straight into the evaluator.
func TestEvaluate_WithCollector(t *testing.T) {
	t.Parallel()

	c := newTestCollector(loadtest.CollectorOptions{})

	for i := 1; i <= 100; i++ {
		r := loadtest.Result{Latency: time.Duration(i) * time.Millisecond}
		if i > 98 {
			r.ErrorClass = loadtest.ClassHTTP5xx
		}

		c.Record(&r)
	}

	c.Tick()

	results := loadtest.Evaluate(c, []domain.Threshold{
		th("http.latency.p95", domain.OpLess, 100),
		th("errors.rate", domain.OpLess, 0.01),
	})

	if !results[0].Passed || results[1].Passed {
		t.Errorf("results %+v", results)
	}
}
