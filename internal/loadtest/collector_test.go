package loadtest_test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
)

func newTestCollector(opts loadtest.CollectorOptions) *loadtest.Collector {
	return loadtest.NewCollector(&opts)
}

// Concurrent workers: merged totals must equal the sum of what they sent.
func TestCollector_ConcurrentTotals(t *testing.T) {
	t.Parallel()

	c := newTestCollector(loadtest.CollectorOptions{})

	const workers, per = 16, 5000

	var wg sync.WaitGroup

	for w := range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range per {
				r := loadtest.Result{
					Operation: "listPets", Step: "browse/list", Status: 200,
					Latency: time.Duration(1+i%50) * time.Millisecond, BytesIn: 100, BytesOut: 10,
					ChecksPassed: 2,
				}
				if i%10 == 0 {
					r.ErrorClass = loadtest.ClassHTTP5xx
					r.Status = 503
					r.ChecksFailed = 1
				}

				if w == 0 && i%100 == 0 {
					r.ContractViolations = 1
				}

				r.Timings.TTFB = time.Millisecond
				c.Record(&r)

				if i%1000 == 0 {
					c.Tick() // ticking while recording must be safe
				}
			}
		}()
	}

	wg.Wait()
	c.Tick()

	s := c.Snapshot()
	total := int64(workers * per)

	if s.Overall.Totals.Requests != total || s.Overall.Latency.Count() != total {
		t.Fatalf("requests %d latency count %d, want %d", s.Overall.Totals.Requests, s.Overall.Latency.Count(), total)
	}

	if got := s.Operations["listPets"].Totals.Requests; got != total {
		t.Errorf("operation total %d", got)
	}

	if got := s.Steps["browse/list"].Totals.Requests; got != total {
		t.Errorf("step total %d", got)
	}

	wantErr := int64(workers * per / 10)
	if s.Overall.Totals.Errors != wantErr || s.Overall.Totals.ByClass[loadtest.ClassHTTP5xx] != wantErr ||
		s.Overall.Totals.ByClass[loadtest.ClassCheckFailed] != wantErr {
		t.Errorf("errors %d byclass %v", s.Overall.Totals.Errors, s.Overall.Totals.ByClass)
	}

	if s.Overall.Totals.ContractViolations != per/100 {
		t.Errorf("violations %d", s.Overall.Totals.ContractViolations)
	}

	if s.Overall.Totals.BytesIn != total*100 || s.Overall.Totals.BytesOut != total*10 {
		t.Errorf("bytes %d/%d", s.Overall.Totals.BytesIn, s.Overall.Totals.BytesOut)
	}

	if s.Status[200].Count()+s.Status[503].Count() != total || s.Status[503].Count() != wantErr {
		t.Error("status histograms do not add up")
	}

	if s.Timings[loadtest.PhaseTTFB].Count() != total || s.Timings[loadtest.PhaseDNS] != nil {
		t.Error("timing histograms: ttfb on every request, no dns")
	}

	var inSeries int64
	for _, b := range s.Series {
		inSeries += b.Requests
	}

	if inSeries != total {
		t.Errorf("time series holds %d requests, want %d", inSeries, total)
	}
}

func TestCollector_WarmupIsExcluded(t *testing.T) {
	t.Parallel()

	c := newTestCollector(loadtest.CollectorOptions{Warmup: 2 * time.Second})

	for range 10 {
		c.Record(&loadtest.Result{Latency: time.Second, Warmup: true})
	}

	c.Record(&loadtest.Result{Latency: time.Millisecond})

	for range 4 {
		c.Tick()
	}

	s := c.Snapshot()
	if s.Overall.Totals.Requests != 1 || s.WarmupRequests != 10 || s.MeasuredSeconds != 2 {
		t.Errorf("requests %d warmup %d measured seconds %d", s.Overall.Totals.Requests, s.WarmupRequests, s.MeasuredSeconds)
	}

	if !s.Series[0].Warmup || !s.Series[1].Warmup || s.Series[2].Warmup {
		t.Error("the first two seconds are warmup")
	}
}

func TestCollector_SecondBuckets(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	c := newTestCollector(loadtest.CollectorOptions{Stats: &stats})

	for i := 1; i <= 100; i++ {
		c.Record(&loadtest.Result{Latency: time.Duration(i) * time.Millisecond, ErrorClass: loadtest.ClassTimeout})
	}

	b := c.Tick()
	if b.Second != 1 || b.Requests != 100 || b.Errors != 100 || b.ErrorsByClass[loadtest.ClassTimeout] != 100 {
		t.Errorf("bucket %+v", b)
	}

	if math.Abs(b.P50-50) > 0.1 || math.Abs(b.P99-99) > 0.2 || math.Abs(b.Max-100) > 0.1 || math.Abs(b.Mean-50.5) > 0.1 {
		t.Errorf("p50 %v p99 %v max %v mean %v", b.P50, b.P99, b.Max, b.Mean)
	}

	var heat uint32
	for _, n := range b.Heat {
		heat += n
	}

	if heat != 100 || len(b.Heat) == 0 || loadtest.HeatBinUpperMs(0) <= 0 {
		t.Errorf("heat bins hold %d", heat)
	}

	if empty := c.Tick(); empty.Requests != 0 || empty.Second != 2 || empty.P99 != 0 {
		t.Errorf("an empty second must be empty: %+v", empty)
	}
}

func TestCollector_SamplesAndHealth(t *testing.T) {
	t.Parallel()

	c := newTestCollector(loadtest.CollectorOptions{SampleN: 3})

	for i := 1; i <= 20; i++ {
		r := loadtest.Result{Latency: time.Duration(i) * time.Millisecond, Detail: &loadtest.Detail{URL: "/x"}}
		if i%5 == 0 {
			r.ErrorClass = loadtest.ClassReset
		}

		c.Record(&r)
	}

	c.Record(&loadtest.Result{Latency: time.Hour}) // no detail: not a sample

	c.Tick()

	s := c.Snapshot()
	if len(s.Slowest) != 3 || s.Slowest[0].Latency != 20*time.Millisecond || s.Slowest[2].Latency != 18*time.Millisecond {
		t.Errorf("slowest %v", s.Slowest)
	}

	if len(s.FirstFailed) != 3 || s.FirstFailed[0].Latency != 5*time.Millisecond {
		t.Errorf("first failed %v", s.FirstFailed)
	}

	if len(s.Health) != 1 || s.Health[0].Goroutines <= 0 {
		t.Errorf("health %+v", s.Health)
	}
}

func TestCollector_UntrustworthyWhenSchedulerLags(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	c := newTestCollector(loadtest.CollectorOptions{Stats: &stats, LagThreshold: 20 * time.Millisecond})

	// A modest plan is kept up with.
	calm := loadtest.OpenConfig{
		Stages: []domain.Stage{stage(300*time.Millisecond, 50, 0)}, Weights: []int{1}, Stats: &stats,
		Run: func(context.Context, loadtest.IterationInfo) {},
	}
	if err := loadtest.RunOpen(context.Background(), &calm); err != nil {
		t.Fatal(err)
	}

	c.Tick()

	if s := c.Snapshot(); s.Untrustworthy || s.UntrustworthyReason != "" {
		t.Errorf("a generator that keeps up is trustworthy: %s", s.UntrustworthyReason)
	}

	// An impossible rate cannot be dispatched on time, so the lag grows.
	hot := loadtest.OpenConfig{
		Stages: []domain.Stage{stage(300*time.Millisecond, 20_000_000, 0)}, Weights: []int{1}, Stats: &stats,
		Run: func(context.Context, loadtest.IterationInfo) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_ = loadtest.RunOpen(ctx, &hot)

	c.Tick()

	s := c.Snapshot()
	if !s.Untrustworthy || s.MaxLag < 20*time.Millisecond || s.UntrustworthyReason == "" {
		t.Errorf("untrustworthy=%v max lag %v", s.Untrustworthy, s.MaxLag)
	}
}

func TestCollector_RunTicksUntilCancelled(t *testing.T) {
	t.Parallel()

	c := newTestCollector(loadtest.CollectorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)

	defer cancel()

	c.Run(ctx)

	if n := len(c.Snapshot().Series); n != 3 {
		t.Errorf("%d buckets, want 2 ticks plus the final one", n)
	}
}

func TestCollector_Metrics(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	c := newTestCollector(loadtest.CollectorOptions{Stats: &stats})

	if _, ok := c.Metric(domain.MetricErrorsRate, ""); ok {
		t.Error("no data before any request")
	}

	for i := 1; i <= 100; i++ {
		r := loadtest.Result{Operation: "a", Step: "s1", Latency: time.Duration(i) * time.Millisecond, ChecksPassed: 1}
		if i > 90 {
			r.Operation, r.Step = "b", "s2"
			r.ErrorClass, r.ChecksFailed, r.ChecksPassed, r.ContractViolations = loadtest.ClassHTTP5xx, 1, 0, 1
		}

		c.Record(&r)
	}

	c.Tick()
	c.Tick()

	near := func(metric, scope string, want float64) {
		t.Helper()

		got, ok := c.Metric(metric, scope)
		if !ok || math.Abs(got-want) > want*0.01+1e-9 {
			t.Errorf("%s[%q] = %v (%v), want %v", metric, scope, got, ok, want)
		}
	}

	near(domain.MetricErrorsRate, "", 0.1)
	near(domain.MetricErrorsRate, "a", 0)
	near(domain.MetricErrorsRate, "b", 1)
	near(domain.MetricErrorsRate, "s2", 1)
	near(domain.MetricErrorsCount, "", 10)
	near(domain.MetricChecksRate, "", 0.9)
	near(domain.MetricContractViolations, "", 10)
	near("http.latency.p50", "", 50)
	near("http.latency.p95", "", 95)
	near("http.latency.p99.9", "", 100)
	near("http.latency.p50", "a", 45)
	near(domain.MetricLatencyAvg, "", 50.5)
	near(domain.MetricLatencyMin, "", 1)
	near(domain.MetricLatencyMax, "", 100)
	near(domain.MetricThroughputRPS, "", 50) // 100 requests over 2 measured seconds
	near(domain.MetricDroppedCount, "", 0)

	if _, ok := c.Metric(domain.MetricErrorsRate, "unknown"); ok {
		t.Error("an unknown scope has no data")
	}

	if _, ok := c.Metric("nonsense", ""); ok {
		t.Error("an unknown metric has no data")
	}

	if _, ok := newTestCollector(loadtest.CollectorOptions{}).Metric(domain.MetricDroppedCount, ""); ok {
		t.Error("dropped.count needs scheduler stats")
	}

	if _, ok := c.Metric(domain.MetricChecksRate, "unknown"); ok {
		t.Error("no checks, no data")
	}
}
