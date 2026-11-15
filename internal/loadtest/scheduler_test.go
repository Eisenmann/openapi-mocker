package loadtest_test

import (
	"context"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/hdr"
)

func stage(d time.Duration, rate, vus int) domain.Stage {
	return domain.Stage{Duration: domain.Duration(d), Rate: rate, VUs: vus}
}

// A fixed 50 ms target: achieved rate must be within 1% of the requested one.
func TestRunOpen_AchievedRate(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	var n atomic.Int64

	cfg := loadtest.OpenConfig{
		Stages:  []domain.Stage{stage(4*time.Second, 200, 0)},
		Weights: []int{1},
		Run: func(ctx context.Context, _ loadtest.IterationInfo) {
			n.Add(1)

			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
			}
		},
		Stats: &stats,
	}

	start := time.Now()
	if err := loadtest.RunOpen(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	// Ramp from 0 to 200/s over 4 s is 400 iterations; the run ends once the
	// last 50 ms iterations finish.
	want := 400.0
	if got := float64(n.Load()); math.Abs(got-want)/want > 0.01 {
		t.Errorf("ran %v iterations, want %v +-1%%", got, want)
	}

	if stats.Started() != n.Load() || stats.Completed() != n.Load() || stats.Dropped() != 0 || stats.InFlight() != 0 {
		t.Errorf("stats: started %d completed %d dropped %d in flight %d",
			stats.Started(), stats.Completed(), stats.Dropped(), stats.InFlight())
	}

	if el := time.Since(start); el < 3900*time.Millisecond || el > 5*time.Second {
		t.Errorf("run took %v", el)
	}
}

func TestRunOpen_ConstantRateAfterRamp(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex

	var times []time.Duration

	begin := time.Now()
	cfg := loadtest.OpenConfig{
		// 1 s ramp 0 -> 100, then hold 100/s for 2 s: 50 + 200 iterations.
		Stages:  []domain.Stage{stage(time.Second, 100, 0), stage(2*time.Second, 100, 0)},
		Weights: []int{1},
		Run: func(_ context.Context, info loadtest.IterationInfo) {
			mu.Lock()
			times = append(times, info.Intended.Sub(begin))
			mu.Unlock()
		},
	}

	if err := loadtest.RunOpen(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	if len(times) != 250 {
		t.Errorf("got %d iterations, want 250", len(times))
	}

	// During the hold the gaps between intended starts are 10 ms.
	slices.Sort(times)

	held := 0

	for i := 1; i < len(times); i++ {
		if times[i-1] > time.Second+50*time.Millisecond {
			if gap := times[i] - times[i-1]; gap < 9*time.Millisecond || gap > 11*time.Millisecond {
				t.Fatalf("gap %v in the hold phase", gap)
			}

			held++
		}
	}

	if held < 150 {
		t.Errorf("only %d gaps checked", held)
	}
}

// Coordinated omission: a 1 s stall of the target must show in p99 because
// latency is measured from the intended start and arrivals keep coming.
func TestRunOpen_StallShowsInP99(t *testing.T) {
	t.Parallel()

	begin := time.Now()
	stallFrom, stallTo := begin.Add(time.Second), begin.Add(2*time.Second)

	var mu sync.Mutex

	h := hdr.New()

	cfg := loadtest.OpenConfig{
		Stages:  []domain.Stage{stage(4*time.Second, 100, 0)},
		Weights: []int{1},
		Run: func(_ context.Context, info loadtest.IterationInfo) {
			if now := time.Now(); now.After(stallFrom) && now.Before(stallTo) {
				time.Sleep(time.Until(stallTo)) // the target is frozen
			}

			time.Sleep(5 * time.Millisecond)

			mu.Lock()
			h.RecordDuration(time.Since(info.Intended))
			mu.Unlock()
		},
	}

	if err := loadtest.RunOpen(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	if p99 := time.Duration(h.ValueAtPercentile(99)) * time.Microsecond; p99 < 700*time.Millisecond {
		t.Errorf("p99 is %v; the 1 s stall is hidden", p99)
	}

	if p50 := time.Duration(h.ValueAtPercentile(50)) * time.Microsecond; p50 > 100*time.Millisecond {
		t.Errorf("p50 is %v, most iterations were not stalled", p50)
	}
}

func TestRunOpen_DropsAboveMaxInFlight(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	cfg := loadtest.OpenConfig{
		Stages:      []domain.Stage{stage(time.Second, 100, 0)},
		Weights:     []int{1},
		MaxInFlight: 2,
		Run:         func(_ context.Context, _ loadtest.IterationInfo) { time.Sleep(300 * time.Millisecond) },
		Stats:       &stats,
	}

	if err := loadtest.RunOpen(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	if stats.Dropped() < 30 || stats.Started()+stats.Dropped() != 50 {
		t.Errorf("started %d dropped %d (50 arrivals expected)", stats.Started(), stats.Dropped())
	}
}

func TestRunOpen_WarmupAndWeights(t *testing.T) {
	t.Parallel()

	var warm, cold, first, second atomic.Int64

	cfg := loadtest.OpenConfig{
		Stages:  []domain.Stage{stage(2*time.Second, 1000, 0)},
		Weights: []int{3, 1},
		Warmup:  time.Second,
		Seed:    7,
		Run: func(_ context.Context, info loadtest.IterationInfo) {
			if info.Warmup {
				warm.Add(1)
			} else {
				cold.Add(1)
			}

			if info.Scenario == 0 {
				first.Add(1)
			} else {
				second.Add(1)
			}
		},
	}

	if err := loadtest.RunOpen(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	// 0 -> 1000/s over 2 s: 250 iterations in the first second, 750 in the second.
	if warm.Load() < 245 || warm.Load() > 255 || cold.Load() < 745 || cold.Load() > 755 {
		t.Errorf("warmup %d, measured %d", warm.Load(), cold.Load())
	}

	ratio := float64(first.Load()) / float64(second.Load())
	if ratio < 2.6 || ratio > 3.5 {
		t.Errorf("scenario split %d:%d, want about 3:1", first.Load(), second.Load())
	}
}

func TestRunOpen_CancelStopsWithinGracefulStop(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	var stopped atomic.Int64

	cfg := loadtest.OpenConfig{
		Stages:       []domain.Stage{stage(time.Minute, 6000, 0)},
		Weights:      []int{1},
		GracefulStop: 200 * time.Millisecond,
		Run: func(ctx context.Context, _ loadtest.IterationInfo) {
			<-ctx.Done()
			stopped.Add(1)
		},
	}

	time.AfterFunc(500*time.Millisecond, cancel)

	start := time.Now()
	if err := loadtest.RunOpen(ctx, &cfg); err != nil {
		t.Fatal(err)
	}

	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("took %v to stop", el)
	}

	if stopped.Load() == 0 {
		t.Error("iterations must be cancelled after the graceful stop")
	}
}

func TestSchedulers_RejectBadConfig(t *testing.T) {
	t.Parallel()

	run := func(context.Context, loadtest.IterationInfo) {}
	ctx := context.Background()
	stages := []domain.Stage{stage(time.Second, 1, 1)}

	if loadtest.RunOpen(ctx, &loadtest.OpenConfig{Stages: stages, Weights: []int{1}}) == nil {
		t.Error("open: no run func")
	}

	if loadtest.RunOpen(ctx, &loadtest.OpenConfig{Stages: stages, Weights: []int{0}, Run: run}) == nil {
		t.Error("open: zero weight")
	}

	if loadtest.RunOpen(ctx, &loadtest.OpenConfig{Weights: []int{1}, Run: run}) == nil {
		t.Error("open: no stages")
	}

	if loadtest.RunClosed(ctx, &loadtest.ClosedConfig{Stages: stages, Run: run}) == nil {
		t.Error("closed: no weights")
	}

	if loadtest.RunClosed(ctx, &loadtest.ClosedConfig{Weights: []int{1}, Run: run}) == nil {
		t.Error("closed: no stages")
	}

	if loadtest.RunIterations(ctx, &loadtest.IterationsConfig{Weights: []int{1}, Run: run, VUs: 1}) == nil {
		t.Error("iterations: no count")
	}

	if loadtest.RunIterations(ctx, &loadtest.IterationsConfig{Weights: nil, Run: run, VUs: 1, Iterations: 1}) == nil {
		t.Error("iterations: no weights")
	}
}

// The VU count over time must follow the stages.
func TestRunClosed_VUsFollowStages(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	cfg := loadtest.ClosedConfig{
		Stages: []domain.Stage{
			stage(time.Second, 0, 20), stage(time.Second, 0, 20), stage(time.Second, 0, 0),
		},
		Weights: []int{1},
		Run: func(ctx context.Context, _ loadtest.IterationInfo) {
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
			}
		},
		Stats: &stats,
	}

	var mu sync.Mutex

	type point struct {
		at  time.Duration
		vus int64
	}

	var series []point

	begin := time.Now()
	stopSampling := make(chan struct{})
	sampled := make(chan struct{})

	go func() {
		defer close(sampled)

		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()

		for {
			select {
			case <-stopSampling:
				return
			case <-tk.C:
				mu.Lock()
				series = append(series, point{time.Since(begin), stats.ActiveVUs()})
				mu.Unlock()
			}
		}
	}()

	if err := loadtest.RunClosed(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	close(stopSampling)
	<-sampled

	checked := 0

	for _, p := range series {
		var want float64

		switch s := p.at.Seconds(); {
		case s < 1:
			want = 20 * s
		case s < 2:
			want = 20
		case s < 3:
			want = 20 * (3 - s)
		}

		if p.at > 3*time.Second {
			continue
		}

		if math.Abs(float64(p.vus)-want) > 3 {
			t.Errorf("at %v: %d VUs, want about %.1f", p.at, p.vus, want)
		}

		checked++
	}

	if checked < 25 {
		t.Errorf("only %d samples", checked)
	}

	if stats.ActiveVUs() != 0 || stats.Started() < 500 {
		t.Errorf("active VUs %d, iterations %d", stats.ActiveVUs(), stats.Started())
	}
}

func TestRunIterations_SharedAndPerVU(t *testing.T) {
	t.Parallel()

	for _, perVU := range []bool{false, true} {
		var n atomic.Int64

		vus := map[int]bool{}

		var mu sync.Mutex

		cfg := loadtest.IterationsConfig{
			VUs: 4, Iterations: 25, PerVU: perVU, Weights: []int{1},
			Run: func(_ context.Context, info loadtest.IterationInfo) {
				n.Add(1)
				time.Sleep(time.Millisecond)
				mu.Lock()
				vus[info.VU] = true
				mu.Unlock()
			},
		}

		if err := loadtest.RunIterations(context.Background(), &cfg); err != nil {
			t.Fatal(err)
		}

		want := int64(25)
		if perVU {
			want = 100
		}

		if n.Load() != want || len(vus) != 4 {
			t.Errorf("perVU=%v: %d iterations on %d VUs, want %d on 4", perVU, n.Load(), len(vus), want)
		}
	}
}

func TestRunIterations_Cancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cfg := loadtest.IterationsConfig{
		VUs: 2, Iterations: 1_000_000, Weights: []int{1}, GracefulStop: 100 * time.Millisecond,
		Run: func(ctx context.Context, _ loadtest.IterationInfo) {
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
			}
		},
	}

	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	if err := loadtest.RunIterations(ctx, &cfg); err != nil {
		t.Fatal(err)
	}

	if time.Since(start) > 2*time.Second {
		t.Error("a cancelled run must stop")
	}
}

func TestStats_TakeMaxLag(t *testing.T) {
	t.Parallel()

	var stats loadtest.Stats

	cfg := loadtest.OpenConfig{
		Stages: []domain.Stage{stage(200*time.Millisecond, 100, 0)}, Weights: []int{1}, Stats: &stats,
		Run: func(context.Context, loadtest.IterationInfo) {},
	}

	if err := loadtest.RunOpen(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}

	if lag := stats.TakeMaxLag(); lag <= 0 || lag > 100*time.Millisecond {
		t.Errorf("lag %v", lag)
	}

	if stats.TakeMaxLag() != 0 {
		t.Error("TakeMaxLag must reset")
	}
}
