package loadtest

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// vuTick is how often the closed model compares running VUs with the plan.
const vuTick = 25 * time.Millisecond

// ClosedConfig configures a VU-based run.
type ClosedConfig struct {
	// Stages ramp the number of virtual users linearly from the previous
	// stage's VUs (0 before the first stage) to this stage's VUs.
	Stages       []domain.Stage
	Weights      []int
	Warmup       time.Duration
	GracefulStop time.Duration
	Seed         uint64
	Run          Iteration
	Stats        *Stats
}

// IterationsConfig configures a run of a fixed number of iterations.
type IterationsConfig struct {
	VUs int
	// Iterations is shared by all VUs, or per VU when PerVU is set.
	Iterations   int
	PerVU        bool
	Weights      []int
	Warmup       time.Duration
	GracefulStop time.Duration
	Seed         uint64
	Run          Iteration
	Stats        *Stats
}

// vuTarget is the planned number of VUs at elapsed seconds.
func vuTarget(stages []domain.Stage, elapsed float64) (target int, done bool) {
	var start, prev float64

	for _, st := range stages {
		dur := seconds(st.Duration.Std())
		next := float64(st.VUs)

		if elapsed < start+dur {
			frac := (elapsed - start) / dur

			return int(math.Round(prev + (next-prev)*frac)), false
		}

		start += dur
		prev = next
	}

	return 0, true
}

// vuPool starts and stops virtual users.
type vuPool struct {
	runner  *iterRunner
	stats   *Stats
	weights []int
	seed    uint64
	begin   time.Time
	warmup  time.Duration
	stops   []chan struct{}
	wg      sync.WaitGroup
	nextID  int
	// budget limits the iterations of a count-based run; nil means unlimited.
	budget *atomic.Int64
	// perVU is the iteration count of each VU in a per-VU run (0 = unlimited).
	perVU int
}

func (v *vuPool) spawn() {
	stop := make(chan struct{})
	v.stops = append(v.stops, stop)
	v.nextID++
	id := v.nextID
	v.stats.activeVUs.Add(1)
	v.wg.Add(1)

	go func() {
		defer v.wg.Done()
		defer v.stats.activeVUs.Add(-1)

		v.loop(id, stop)
	}()
}

func (v *vuPool) loop(id int, stop <-chan struct{}) {
	pick, err := newPicker(v.weights, v.seed+uint64(id)) //nolint:gosec // id is a small positive counter
	if err != nil {
		return
	}

	for n := 0; v.perVU == 0 || n < v.perVU; n++ {
		select {
		case <-stop:
			return
		default:
		}

		if v.budget != nil && v.budget.Add(-1) < 0 {
			return
		}

		now := time.Now()

		var info IterationInfo

		info.Scenario = pick.pick()
		info.VU = id
		info.Intended = now
		info.Warmup = now.Sub(v.begin) < v.warmup

		// One iteration at a time per VU: wait for it before the next.
		finished := make(chan struct{})

		v.runner.start(info, func() { close(finished) })
		<-finished
	}
}

// resize starts or stops VUs until n are running. The newest VUs are stopped
// first; a stopped VU finishes its current iteration.
func (v *vuPool) resize(n int) {
	for len(v.stops) < n {
		v.spawn()
	}

	for len(v.stops) > n {
		last := len(v.stops) - 1
		close(v.stops[last])
		v.stops = v.stops[:last]
	}
}

func newVUPool(runner *iterRunner, stats *Stats, weights []int, seed uint64, warmup time.Duration) *vuPool {
	v := new(vuPool)
	v.runner = runner
	v.stats = stats
	v.weights = weights
	v.seed = seed
	v.warmup = warmup
	v.begin = time.Now()

	return v
}

// RunClosed runs virtual users that loop their scenarios, following the VU
// stages. A slow target lowers the request rate, which is what a closed model
// is for (and why latencies here start at the real send time).
func RunClosed(ctx context.Context, cfg *ClosedConfig) error {
	if cfg.Run == nil || len(cfg.Stages) == 0 {
		return ErrSchedulerConfig
	}

	_, err := newPicker(cfg.Weights, cfg.Seed)
	if err != nil {
		return err
	}

	stats := cfg.Stats
	if stats == nil {
		stats = new(Stats)
	}

	runner := newIterRunner(ctx, cfg.Run, stats, cfg.GracefulStop)
	pool := newVUPool(runner, stats, cfg.Weights, cfg.Seed, cfg.Warmup)
	ticker := time.NewTicker(vuTick)

	defer ticker.Stop()

loop:
	for {
		target, done := vuTarget(cfg.Stages, time.Since(pool.begin).Seconds())
		if done {
			break
		}

		pool.resize(target)

		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
		}
	}

	pool.resize(0)
	runner.wait()
	pool.wg.Wait()

	return nil
}

// RunIterations runs a fixed number of iterations with a fixed number of VUs.
func RunIterations(ctx context.Context, cfg *IterationsConfig) error {
	if cfg.Run == nil || cfg.VUs <= 0 || cfg.Iterations <= 0 {
		return ErrSchedulerConfig
	}

	_, err := newPicker(cfg.Weights, cfg.Seed)
	if err != nil {
		return err
	}

	stats := cfg.Stats
	if stats == nil {
		stats = new(Stats)
	}

	runner := newIterRunner(ctx, cfg.Run, stats, cfg.GracefulStop)
	pool := newVUPool(runner, stats, cfg.Weights, cfg.Seed, cfg.Warmup)

	if cfg.PerVU {
		pool.perVU = cfg.Iterations
	} else {
		pool.budget = new(atomic.Int64)
		pool.budget.Store(int64(cfg.Iterations))
	}

	pool.resize(cfg.VUs)

	finished := make(chan struct{})

	go func() {
		pool.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-ctx.Done():
		pool.resize(0)
	}

	runner.wait()
	pool.wg.Wait()

	return nil
}
