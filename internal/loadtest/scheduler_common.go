package loadtest

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSchedulerConfig is returned for a scheduler configuration that cannot
// run (no iteration function, no or non-positive scenario weights).
var ErrSchedulerConfig = errors.New("loadtest: invalid scheduler configuration")

// DefaultMaxInFlight bounds concurrent iterations of an open model when the
// plan sets no limit.
const DefaultMaxInFlight = 10_000

// seedMix decorrelates the two words of the PCG seed (golden ratio constant).
const seedMix = 0x9e3779b97f4a7c15

// Stats are live counters of a scheduler; the collector samples them every
// second. All methods are safe for concurrent use.
type Stats struct {
	started   atomic.Int64
	completed atomic.Int64
	dropped   atomic.Int64
	inFlight  atomic.Int64
	activeVUs atomic.Int64
	maxLagNs  atomic.Int64
}

// Started is the number of iterations that were started.
func (s *Stats) Started() int64 { return s.started.Load() }

// Completed is the number of iterations that finished.
func (s *Stats) Completed() int64 { return s.completed.Load() }

// Dropped is the number of open-model iterations skipped because maxInFlight
// was reached.
func (s *Stats) Dropped() int64 { return s.dropped.Load() }

// InFlight is the number of iterations running now.
func (s *Stats) InFlight() int64 { return s.inFlight.Load() }

// ActiveVUs is the number of virtual users running now (closed models).
func (s *Stats) ActiveVUs() int64 { return s.activeVUs.Load() }

// TakeMaxLag returns the largest delay between an iteration's intended start
// and its real start since the last call, and resets it. A growing lag means
// the load generator itself cannot keep up with the plan.
func (s *Stats) TakeMaxLag() time.Duration { return time.Duration(s.maxLagNs.Swap(0)) }

func (s *Stats) observeLag(lag time.Duration) {
	ns := int64(lag)

	for {
		cur := s.maxLagNs.Load()
		if ns <= cur || s.maxLagNs.CompareAndSwap(cur, ns) {
			return
		}
	}
}

// picker chooses scenarios by weight. It is not safe for concurrent use.
type picker struct {
	cum   []int
	total int
	rng   *rand.Rand
}

func newPicker(weights []int, seed uint64) (*picker, error) {
	p := new(picker)
	p.rng = rand.New(rand.NewPCG(seed, seed^seedMix)) //nolint:gosec // load shaping, not security

	for _, w := range weights {
		if w <= 0 {
			return nil, ErrSchedulerConfig
		}

		p.total += w
		p.cum = append(p.cum, p.total)
	}

	if p.total == 0 {
		return nil, ErrSchedulerConfig
	}

	return p, nil
}

func (p *picker) pick() int {
	n := p.rng.IntN(p.total)

	for i, c := range p.cum {
		if n < c {
			return i
		}
	}

	return len(p.cum) - 1
}

// iterRunner starts iterations under a context that outlives the run's own
// cancellation by the graceful-stop period.
type iterRunner struct {
	run      Iteration
	stats    *Stats
	iterCtx  context.Context //nolint:containedctx // detached context shared by all iterations of a run
	cancel   context.CancelFunc
	graceful time.Duration

	// mu guards closed and the wg.Add calls: a VU may try to start an
	// iteration just as the run ends, and Add must not race with Wait.
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newIterRunner(parent context.Context, run Iteration, stats *Stats, graceful time.Duration) *iterRunner {
	r := new(iterRunner)
	r.run = run
	r.stats = stats
	r.graceful = graceful
	r.iterCtx, r.cancel = context.WithCancel(context.WithoutCancel(parent))

	return r
}

// start runs one iteration in its own goroutine.
// After the run has begun to wait, no new iteration starts; release is still
// called so the caller does not block.
func (r *iterRunner) start(info IterationInfo, release func()) {
	r.mu.Lock()

	if r.closed {
		r.mu.Unlock()

		if release != nil {
			release()
		}

		return
	}

	r.wg.Add(1)
	r.mu.Unlock()

	r.stats.started.Add(1)
	r.stats.inFlight.Add(1)
	r.stats.observeLag(time.Since(info.Intended))

	go func() {
		defer r.wg.Done()
		defer func() {
			r.stats.inFlight.Add(-1)
			r.stats.completed.Add(1)

			if release != nil {
				release()
			}
		}()

		r.run(r.iterCtx, info)
	}()
}

// wait lets running iterations finish for the graceful-stop period, then
// cancels them and waits for them to return.
func (r *iterRunner) wait() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()

	done := make(chan struct{})

	go func() {
		r.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(r.graceful)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
		r.cancel()
		<-done
	}

	r.cancel()
}

// sleepUntil waits for t. It reports false when ctx ended first.
func sleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func seconds(d time.Duration) float64 { return d.Seconds() }

func fromSeconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
