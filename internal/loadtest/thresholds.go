package loadtest

import (
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Process exit codes of a load run.
const (
	ExitPass            = 0
	ExitThresholdFailed = 1
	ExitPlanError       = 2
)

// MetricSource gives the current value of a metric for a scope; ok is false
// when nothing was measured yet. *Collector implements it.
type MetricSource interface {
	Metric(metric, scope string) (value float64, ok bool)
}

// ThresholdResult is the outcome of one threshold.
type ThresholdResult struct {
	Threshold domain.Threshold
	// Observed is the measured value; meaningless when HasData is false.
	Observed float64
	// HasData is false when the metric was never measured (for example no
	// request reached the scope); such a threshold does not pass.
	HasData bool
	Passed  bool
}

// Compare applies a threshold operator.
func Compare(observed float64, op domain.ThresholdOp, value float64) bool {
	switch op {
	case domain.OpLess:
		return observed < value
	case domain.OpLessEqual:
		return observed <= value
	case domain.OpGreater:
		return observed > value
	case domain.OpGreaterEqual:
		return observed >= value
	case domain.OpEqual:
		return observed == value
	case domain.OpNotEqual:
		return observed != value
	default:
		return false
	}
}

func evalOne(src MetricSource, t *domain.Threshold) ThresholdResult {
	var r ThresholdResult

	r.Threshold = *t
	r.Observed, r.HasData = src.Metric(t.Metric, t.Scope)
	r.Passed = r.HasData && Compare(r.Observed, t.Op, t.Value)

	return r
}

// Evaluate checks every threshold against the final figures of a run.
func Evaluate(src MetricSource, thresholds []domain.Threshold) []ThresholdResult {
	out := make([]ThresholdResult, 0, len(thresholds))

	for i := range thresholds {
		out = append(out, evalOne(src, &thresholds[i]))
	}

	return out
}

// CheckAbort is called every second during a run. It returns the first
// abortOnFail threshold that has data, is past its abort delay (elapsed is
// the time since the measured period began) and currently fails, or nil.
// Thresholds without data are not held against the run yet.
func CheckAbort(src MetricSource, thresholds []domain.Threshold, elapsed time.Duration) *ThresholdResult {
	for i := range thresholds {
		t := &thresholds[i]
		if !t.AbortOnFail || elapsed < t.AbortDelay.Std() {
			continue
		}

		r := evalOne(src, t)
		if r.HasData && !r.Passed {
			return &r
		}
	}

	return nil
}

// Passed reports whether every threshold passed.
func Passed(results []ThresholdResult) bool {
	for i := range results {
		if !results[i].Passed {
			return false
		}
	}

	return true
}

// ExitCode maps a finished run to the process exit code: 0 when every
// threshold passed, 1 when one failed or the run was aborted by one.
// Plan and target problems are ExitPlanError and decided before a run starts.
func ExitCode(results []ThresholdResult, aborted bool) int {
	if aborted || !Passed(results) {
		return ExitThresholdFailed
	}

	return ExitPass
}
