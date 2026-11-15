package loadtest

import (
	"runtime/metrics"
	"time"
)

// Names of the runtime metrics read for generator health.
const (
	metricCPU        = "/cpu/classes/total:cpu-seconds"
	metricGoroutines = "/sched/goroutines:goroutines"
	metricGCPauses   = "/gc/pauses:seconds"
	metricSchedLat   = "/sched/latencies:seconds"

	healthQuantile = 0.99
	idxGCPauses    = 2
	idxSchedLat    = 3
	// roundUp makes the target rank round up; openEndedBound marks the last,
	// unbounded histogram bucket.
	roundUp        = 0.999999
	openEndedBound = 1e9
	msPerSecond    = 1000
)

// HealthSample describes the load generator itself during one second, so a
// slow result can be told apart from a slow generator.
type HealthSample struct {
	Second int
	// CPUCores is the average number of cores the process used.
	CPUCores   float64
	Goroutines int
	// GCPauseMaxMs is the longest stop-the-world pause in the interval.
	GCPauseMaxMs float64
	// SchedLatencyP99Ms is the p99 time goroutines waited to be scheduled.
	SchedLatencyP99Ms float64
	// LagMs is the largest delay between an iteration's intended and real
	// start in the interval.
	LagMs float64
}

// healthSampler reads runtime/metrics and keeps the previous readings to
// report per-interval values.
type healthSampler struct {
	samples  []metrics.Sample
	lastAt   time.Time
	lastCPU  float64
	lastGC   []uint64
	lastSche []uint64
}

func newHealthSampler() *healthSampler {
	h := new(healthSampler)

	for _, name := range []string{metricCPU, metricGoroutines, metricGCPauses, metricSchedLat} {
		var s metrics.Sample

		s.Name = name
		h.samples = append(h.samples, s)
	}

	h.read()
	h.lastAt = time.Now()
	h.lastCPU = h.cpu()
	h.lastGC = cloneCounts(h.samples[idxGCPauses])
	h.lastSche = cloneCounts(h.samples[idxSchedLat])

	return h
}

func (h *healthSampler) read() { metrics.Read(h.samples) }

func (h *healthSampler) cpu() float64 {
	if h.samples[0].Value.Kind() != metrics.KindFloat64 {
		return 0
	}

	return h.samples[0].Value.Float64()
}

func cloneCounts(s metrics.Sample) []uint64 {
	if s.Value.Kind() != metrics.KindFloat64Histogram {
		return nil
	}

	return append([]uint64(nil), s.Value.Float64Histogram().Counts...)
}

// sample returns the readings for the interval since the previous call.
func (h *healthSampler) sample(second int) HealthSample {
	h.read()

	var out HealthSample

	out.Second = second

	now := time.Now()
	cpu := h.cpu()

	if dt := now.Sub(h.lastAt).Seconds(); dt > 0 {
		out.CPUCores = (cpu - h.lastCPU) / dt
	}

	if h.samples[1].Value.Kind() == metrics.KindUint64 {
		out.Goroutines = int(min(h.samples[1].Value.Uint64(), 1<<31-1)) //nolint:gosec // clamped
	}

	out.GCPauseMaxMs = h.histogramDelta(idxGCPauses, &h.lastGC, 1.0) * msPerSecond
	out.SchedLatencyP99Ms = h.histogramDelta(idxSchedLat, &h.lastSche, healthQuantile) * msPerSecond

	h.lastAt = now
	h.lastCPU = cpu

	return out
}

// histogramDelta returns quantile q of the observations added since last
// (q = 1 is the largest bucket used), as the upper bound of its bucket.
func (h *healthSampler) histogramDelta(idx int, last *[]uint64, q float64) float64 {
	if h.samples[idx].Value.Kind() != metrics.KindFloat64Histogram {
		return 0
	}

	hist := h.samples[idx].Value.Float64Histogram()
	delta := make([]uint64, len(hist.Counts))

	var total uint64

	for i, c := range hist.Counts {
		var prev uint64
		if i < len(*last) {
			prev = (*last)[i]
		}

		if c > prev {
			delta[i] = c - prev
			total += delta[i]
		}
	}

	*last = append((*last)[:0], hist.Counts...)

	return quantileOf(hist.Buckets, delta, total, q)
}

func quantileOf(bounds []float64, counts []uint64, total uint64, q float64) float64 {
	if total == 0 {
		return 0
	}

	target := uint64(float64(total)*q + roundUp)
	target = max(target, 1)

	var seen uint64

	for i, c := range counts {
		seen += c
		if c > 0 && seen >= target {
			upper := bounds[i+1]
			if upper > openEndedBound {
				return bounds[i]
			}

			return upper
		}
	}

	return 0
}
