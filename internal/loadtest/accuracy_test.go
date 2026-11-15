package loadtest_test

import (
	"context"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/engine"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// The accuracy tests run the engine against a server whose behaviour is known
// and compare what the load tester reports with what the server saw. They
// take a few seconds each, so -short skips them.

// serverTimes records how long the server itself took for each request.
type serverTimes struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *serverTimes) add(d time.Duration) {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
}

// percentileMs is the nearest-rank percentile of the recorded times in ms.
func (s *serverTimes) percentileMs(p float64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	sorted := append([]time.Duration(nil), s.d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1

	return float64(sorted[max(rank, 0)]) / float64(time.Millisecond)
}

func (s *serverTimes) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.d)
}

// accuracyPlan ramps to rate in one second, then holds it for dur. A stage
// ramps from the previous rate, so the plan sends rate*(dur+0.5s) requests.
func accuracyPlan(rate int, dur time.Duration) *domain.Plan {
	p := new(domain.Plan)
	p.Name = "accuracy"
	p.Target.BaseURL = "http://x"
	p.Scenarios = []domain.Scenario{{Name: "s", Weight: 1, Steps: []domain.Step{{Method: "GET", Path: "/"}}}}
	p.Load = domain.LoadModel{Kind: domain.LoadOpen, Stages: []domain.Stage{
		{Duration: domain.Duration(time.Second), Rate: rate},
		{Duration: domain.Duration(dur), Rate: rate},
	}}
	p.GracefulStop = domain.Duration(5 * time.Second)

	return p
}

func runAccuracy(t *testing.T, srv *httptest.Server, plan *domain.Plan) *usecase.LoadOutcome {
	t.Helper()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	tg := new(usecase.LoadTarget)
	tg.BaseURL = u

	in := new(usecase.LoadExecution)
	in.RunID = "acc"
	in.ProjectID = "p"
	in.Plan = plan
	in.Target = tg

	out, err := engine.New(new(engine.Config)).Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// generatorHealthy reports whether the run's latency figures may be compared
// with the server's. Latency is measured from the intended start of a request,
// so a generator that falls behind its schedule (a starved CPU, antivirus, a
// slow timer) shows it as latency. It says so itself by marking the run
// untrustworthy, which is the documented behaviour; the comparison is then
// skipped and the reason logged.
func generatorHealthy(t *testing.T, out *usecase.LoadOutcome) bool {
	t.Helper()

	t.Logf("load tester: p50 %.1f ms, p95 %.1f ms, p99 %.1f ms", out.Totals.LatencyP50Ms, out.Totals.LatencyP95Ms, out.Totals.LatencyP99Ms)

	if out.Totals.Untrustworthy {
		t.Logf("the load generator marked its figures untrustworthy (%s); latency accuracy is not compared", out.Totals.UntrustworthyReason)

		return false
	}

	return true
}

// tailTolMs is the tolerance for the 99th percentile. A handful of requests
// out of a thousand are enough to move it, and a Windows desktop (coarse clock,
// antivirus, other processes) stalls that many; p50 and p95 stay strict.
func tailTolMs(base float64) float64 {
	const windowsTailMs = 50

	if runtime.GOOS == "windows" {
		return base + windowsTailMs
	}

	return base
}

// near accepts the loadtester's figure when it is within tol of the server's:
// the client sees the server's time plus a little transport, never less.
func near(t *testing.T, name string, got, server, tolMs, tolRel float64) {
	t.Helper()

	tol := math.Max(tolMs, server*tolRel)
	if got < server-1 || got > server+tol {
		t.Errorf("%s: load tester says %.2f ms, the server saw %.2f ms (allowed +%.2f)", name, got, server, tol)
	}
}

func TestAccuracy_RateAndFixedLatency(t *testing.T) { //nolint:paralleltest // timing: must not share the CPU with other tests.
	if testing.Short() {
		t.Skip("accuracy test takes several seconds")
	}

	const (
		rate = 200
		dur  = 5 * time.Second
	)

	var seen serverTimes

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		start := time.Now()

		time.Sleep(25 * time.Millisecond)
		seen.add(time.Since(start))
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	out := runAccuracy(t, srv, accuracyPlan(rate, dur))

	want := float64(rate) * (dur + time.Second/2).Seconds()
	if got := float64(out.Totals.Requests); math.Abs(got-want) > want*0.05 {
		t.Errorf("sent %v requests, the plan asks for %v (±5%%)", got, want)
	}

	if int64(seen.count()) != out.Totals.Requests {
		t.Errorf("the server saw %d requests, the load tester counted %d", seen.count(), out.Totals.Requests)
	}

	if out.Totals.Errors != 0 || out.Totals.Dropped != 0 {
		t.Errorf("errors %d, dropped %d", out.Totals.Errors, out.Totals.Dropped)
	}

	if !generatorHealthy(t, out) {
		return
	}

	near(t, "p50", out.Totals.LatencyP50Ms, seen.percentileMs(50), 5, 0.15)
	near(t, "p95", out.Totals.LatencyP95Ms, seen.percentileMs(95), 8, 0.2)
	near(t, "p99", out.Totals.LatencyP99Ms, seen.percentileMs(99), tailTolMs(15), 0.3)
}

func TestAccuracy_LognormalLatency(t *testing.T) { //nolint:paralleltest // timing: must not share the CPU with other tests.
	if testing.Short() {
		t.Skip("accuracy test takes several seconds")
	}

	var (
		seen serverTimes
		mu   sync.Mutex
		rng  = rand.New(rand.NewPCG(1, 2))
	)

	// Median 40 ms, sigma 0.5: p95 is about 91 ms and p99 about 128 ms.
	delay := func() time.Duration {
		mu.Lock()
		z := rng.NormFloat64()
		mu.Unlock()

		return time.Duration(40 * math.Exp(0.5*z) * float64(time.Millisecond))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		start := time.Now()

		time.Sleep(delay())
		seen.add(time.Since(start))
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	out := runAccuracy(t, srv, accuracyPlan(150, 6*time.Second))

	if !generatorHealthy(t, out) {
		return
	}

	near(t, "p50", out.Totals.LatencyP50Ms, seen.percentileMs(50), 6, 0.15)
	near(t, "p95", out.Totals.LatencyP95Ms, seen.percentileMs(95), 12, 0.2)
	near(t, "p99", out.Totals.LatencyP99Ms, seen.percentileMs(99), tailTolMs(25), 0.3)

	// The distribution itself is the one the server drew, not a different shape.
	if p95 := out.Totals.LatencyP95Ms; p95 < 70 || p95 > 130 {
		t.Errorf("p95 %.1f ms is far from the expected 91 ms", p95)
	}
}

// TestAccuracy_NoCoordinatedOmission stalls the server for a second in the
// middle of a run. A load generator that waits for each answer before it sends
// the next request records one slow request; an open model keeps sending on
// schedule, so every request that arrived during the stall is slow.
func TestAccuracy_NoCoordinatedOmission(t *testing.T) { //nolint:paralleltest // timing: must not share the CPU with other tests.
	if testing.Short() {
		t.Skip("accuracy test takes several seconds")
	}

	started := time.Now()

	var stalled atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		since := time.Since(started)
		if since > 2*time.Second && since < 3*time.Second {
			stalled.Add(1)
			time.Sleep(3*time.Second - since)
		}

		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	started = time.Now()

	out := runAccuracy(t, srv, accuracyPlan(100, 6*time.Second))

	if stalled.Load() < 50 {
		t.Fatalf("only %d requests arrived during the stall; the plan did not keep its schedule", stalled.Load())
	}

	// About 100 of 650 requests waited 0 to 1 s: p95 is near 0.7 s, p99 near 0.95 s.
	if out.Totals.LatencyP99Ms < 800 {
		t.Errorf("p99 is %.0f ms; the stall should show as slow requests (coordinated omission?)", out.Totals.LatencyP99Ms)
	}

	if out.Totals.LatencyP95Ms < 500 {
		t.Errorf("p95 is %.0f ms; a sixth of the requests waited for the stall", out.Totals.LatencyP95Ms)
	}

	if out.Totals.LatencyP50Ms > 50 {
		t.Errorf("p50 is %.0f ms; most requests were not stalled", out.Totals.LatencyP50Ms)
	}
}
