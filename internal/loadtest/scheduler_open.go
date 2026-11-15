package loadtest

import (
	"context"
	"math"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Constants of the arrival-time quadratic.
const (
	quadTwo   = 2
	quadFour  = 4
	flatSlope = 1e-12
	// arrivalCentre puts the k-th arrival where k+0.5 iterations are expected.
	arrivalCentre = 0.5
)

// OpenConfig configures an arrival-rate run.
type OpenConfig struct {
	// Stages ramp the rate linearly from the previous stage's rate (0 before
	// the first stage) to this stage's Rate, in iterations per second.
	Stages []domain.Stage
	// Weights are the scenario weights, in plan order.
	Weights []int
	// Warmup is excluded from statistics.
	Warmup time.Duration
	// GracefulStop is how long running iterations may continue after the
	// last arrival or after the run is cancelled.
	GracefulStop time.Duration
	// MaxInFlight caps concurrent iterations; arrivals beyond it are dropped
	// and counted. 0 means DefaultMaxInFlight.
	MaxInFlight int
	// Seed makes scenario choice reproducible.
	Seed uint64
	// Run executes one iteration.
	Run Iteration
	// Stats receives live counters; may be nil.
	Stats *Stats
}

// segment is one linear piece of the arrival-rate profile.
type segment struct {
	// start and dur are in seconds.
	start, dur float64
	// r0 and r1 are the rates (iterations per second) at both ends.
	r0, r1 float64
	// cum is the number of iterations before the segment.
	cum float64
}

func (s segment) area() float64 { return (s.r0 + s.r1) / 2 * s.dur }

// rateProfile turns the stages into the times at which iterations are due.
type rateProfile struct {
	segs []segment
}

func newRateProfile(stages []domain.Stage) *rateProfile {
	p := new(rateProfile)

	var start, cum, prev float64

	for _, st := range stages {
		var s segment

		s.start = start
		s.dur = seconds(st.Duration.Std())
		s.r0 = prev
		s.r1 = float64(st.Rate)
		s.cum = cum
		p.segs = append(p.segs, s)

		start += s.dur
		cum += s.area()
		prev = s.r1
	}

	return p
}

// timeOf is the time, in seconds from the start, at which the n-th expected
// arrival (n counted from 0 as a fractional number) happens. It reports false
// when the profile ends before n arrivals.
func (p *rateProfile) timeOf(n float64) (float64, bool) {
	for _, s := range p.segs {
		area := s.area()
		if n >= s.cum+area {
			continue
		}

		m := n - s.cum
		a := (s.r1 - s.r0) / (quadTwo * s.dur)

		if math.Abs(a) < flatSlope {
			return s.start + m/s.r0, true
		}

		return s.start + (-s.r0+math.Sqrt(s.r0*s.r0+quadFour*a*m))/(quadTwo*a), true
	}

	return 0, false
}

// RunOpen generates arrivals at the requested rate whatever the target does:
// every iteration gets an intended start time, so a slow target shows up as
// latency instead of as a lower request rate (no coordinated omission). It
// returns when the stages are done and the iterations have finished, or after
// ctx is cancelled and the graceful stop is over.
func RunOpen(ctx context.Context, cfg *OpenConfig) error {
	if cfg.Run == nil || len(cfg.Stages) == 0 {
		return ErrSchedulerConfig
	}

	pick, err := newPicker(cfg.Weights, cfg.Seed)
	if err != nil {
		return err
	}

	stats := cfg.Stats
	if stats == nil {
		stats = new(Stats)
	}

	limit := cfg.MaxInFlight
	if limit <= 0 {
		limit = DefaultMaxInFlight
	}

	runner := newIterRunner(ctx, cfg.Run, stats, cfg.GracefulStop)
	d := new(dispatcher)
	d.cfg = cfg
	d.pick = pick
	d.stats = stats
	d.slots = make(chan struct{}, limit)
	d.runner = runner
	d.begin = time.Now()
	d.dispatch(ctx, newRateProfile(cfg.Stages))

	runner.wait()

	return nil
}

// dispatcher hands out iterations at their intended times.
type dispatcher struct {
	cfg    *OpenConfig
	pick   *picker
	stats  *Stats
	slots  chan struct{}
	runner *iterRunner
	begin  time.Time
}

func (d *dispatcher) dispatch(ctx context.Context, profile *rateProfile) {
	for k := 0; ; k++ {
		// The k-th arrival is due when k+0.5 iterations are expected, which
		// centres arrivals inside their interval.
		t, ok := profile.timeOf(float64(k) + arrivalCentre)
		if !ok {
			return
		}

		intended := d.begin.Add(fromSeconds(t))
		if !sleepUntil(ctx, intended) {
			return
		}

		select {
		case d.slots <- struct{}{}:
			var info IterationInfo

			info.Scenario = d.pick.pick()
			info.VU = len(d.slots)
			info.Intended = intended
			info.Warmup = intended.Sub(d.begin) < d.cfg.Warmup

			d.runner.start(info, func() { <-d.slots })
		default:
			d.stats.dropped.Add(1)
		}
	}
}
