package loadtest

import (
	"container/heap"
	"context"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/hdr"
)

// Collector tuning.
const (
	// heatBinsPerOctave and heatBins describe the latency axis of the heatmap:
	// log-scale bins starting at heatBase.
	heatBinsPerOctave = 2
	heatBins          = 40
	heatBase          = 100 * time.Microsecond

	defaultSampleN          = 10
	defaultLagThreshold     = 100 * time.Millisecond
	maxShards               = 8
	usPerMs                 = 1000.0
	latencyPercentilePrefix = "http.latency.p"
	percentP50              = 50
	percentP90              = 90
	percentP95              = 95
	percentP99              = 99
)

// Timing phases of the breakdown histograms.
const (
	PhaseDNS      = "dns"
	PhaseConnect  = "connect"
	PhaseTLS      = "tls"
	PhaseTTFB     = "ttfb"
	PhaseDownload = "download"
)

var phases = [...]string{PhaseDNS, PhaseConnect, PhaseTLS, PhaseTTFB, PhaseDownload}

// CollectorOptions configures a Collector.
type CollectorOptions struct {
	// Warmup marks the first seconds of the time series as warmup.
	Warmup time.Duration
	// SampleN is how many slowest and how many first-failed requests with
	// detail are kept (default 10).
	SampleN int
	// LagThreshold is the scheduler lag above which the run is marked
	// untrustworthy (default 100 ms).
	LagThreshold time.Duration
	// Stats are the scheduler's live counters; may be nil.
	Stats *Stats
}

// Totals are counters of a set of requests.
type Totals struct {
	Requests           int64
	Errors             int64
	ChecksPassed       int64
	ChecksFailed       int64
	ContractViolations int64
	BytesIn            int64
	BytesOut           int64
	// ByClass counts errors per class, plus failed checks (check_failed) and
	// contract violations (contract_violation).
	ByClass map[ErrorClass]int64
}

func (t *Totals) add(r *Result) {
	t.Requests++
	t.BytesIn += r.BytesIn
	t.BytesOut += r.BytesOut
	t.ChecksPassed += int64(r.ChecksPassed)
	t.ChecksFailed += int64(r.ChecksFailed)
	t.ContractViolations += int64(r.ContractViolations)

	if t.ByClass == nil {
		t.ByClass = make(map[ErrorClass]int64, 1)
	}

	if r.ErrorClass != "" {
		t.Errors++
		t.ByClass[r.ErrorClass]++
	}

	if r.ChecksFailed > 0 {
		t.ByClass[ClassCheckFailed] += int64(r.ChecksFailed)
	}

	if r.ContractViolations > 0 {
		t.ByClass[ClassContractViolation] += int64(r.ContractViolations)
	}
}

func (t *Totals) merge(o *Totals) {
	t.Requests += o.Requests
	t.Errors += o.Errors
	t.ChecksPassed += o.ChecksPassed
	t.ChecksFailed += o.ChecksFailed
	t.ContractViolations += o.ContractViolations
	t.BytesIn += o.BytesIn
	t.BytesOut += o.BytesOut

	if len(o.ByClass) > 0 && t.ByClass == nil {
		t.ByClass = make(map[ErrorClass]int64, len(o.ByClass))
	}

	for k, v := range o.ByClass {
		t.ByClass[k] += v
	}
}

// Breakdown is the latency distribution and the counters of one slice of the
// run (everything, one operation, one step).
type Breakdown struct {
	Latency *hdr.Histogram
	Totals  Totals
}

func newBreakdown() *Breakdown {
	b := new(Breakdown)
	b.Latency = hdr.New()

	return b
}

func (b *Breakdown) add(r *Result) {
	b.Latency.RecordDuration(r.Latency)
	b.Totals.add(r)
}

func (b *Breakdown) merge(o *Breakdown) {
	b.Latency.Merge(o.Latency)
	b.Totals.merge(&o.Totals)
}

// SecondBucket is one second of the run.
type SecondBucket struct {
	Second   int
	Warmup   bool
	Requests int64
	Errors   int64
	// ErrorsByClass includes failed checks and contract violations.
	ErrorsByClass map[ErrorClass]int64
	BytesIn       int64
	BytesOut      int64
	InFlight      int64
	ActiveVUs     int64
	Dropped       int64
	// Latencies are in milliseconds.
	P50, P90, P95, P99, Max, Mean float64
	// Heat counts requests per log-scale latency bin (see HeatBinUpperMs).
	Heat []uint32
}

// HeatBinUpperMs is the upper latency bound of a heatmap bin, in milliseconds.
func HeatBinUpperMs(bin int) float64 {
	return float64(heatBase) / float64(time.Millisecond) * math.Exp2(float64(bin+1)/heatBinsPerOctave)
}

func heatBin(d time.Duration) int {
	if d <= heatBase {
		return 0
	}

	bin := int(math.Log2(float64(d)/float64(heatBase)) * heatBinsPerOctave)

	return min(bin, heatBins-1)
}

// window is what one shard has seen since the last tick.
type window struct {
	requests, errors, bytesIn, bytesOut int64
	byClass                             map[ErrorClass]int64
	latency                             *hdr.Histogram
	heat                                [heatBins]uint32
}

func (w *window) reset() {
	w.requests, w.errors, w.bytesIn, w.bytesOut = 0, 0, 0, 0
	clear(w.byClass)
	w.latency.Reset()
	w.heat = [heatBins]uint32{}
}

// shard is a slice of the collector with its own lock, so recording from many
// goroutines rarely contends.
type shard struct {
	mu      sync.Mutex
	win     window
	overall *Breakdown
	ops     map[string]*Breakdown
	steps   map[string]*Breakdown
	status  map[int]*hdr.Histogram
	timings [len(phases)]*hdr.Histogram
}

func newShard() *shard {
	s := new(shard)
	s.win.byClass = make(map[ErrorClass]int64)
	s.win.latency = hdr.New()
	s.overall = newBreakdown()
	s.ops = make(map[string]*Breakdown)
	s.steps = make(map[string]*Breakdown)
	s.status = make(map[int]*hdr.Histogram)

	return s
}

func breakdownFor(m map[string]*Breakdown, key string) *Breakdown {
	b, ok := m[key]
	if !ok {
		b = newBreakdown()
		m[key] = b
	}

	return b
}

func (s *shard) record(r *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.win.requests++
	s.win.bytesIn += r.BytesIn
	s.win.bytesOut += r.BytesOut
	s.win.latency.RecordDuration(r.Latency)
	s.win.heat[heatBin(r.Latency)]++

	if r.ErrorClass != "" {
		s.win.errors++
		s.win.byClass[r.ErrorClass]++
	}

	if r.ChecksFailed > 0 {
		s.win.byClass[ClassCheckFailed] += int64(r.ChecksFailed)
	}

	if r.ContractViolations > 0 {
		s.win.byClass[ClassContractViolation] += int64(r.ContractViolations)
	}

	s.overall.add(r)

	if r.Operation != "" {
		breakdownFor(s.ops, r.Operation).add(r)
	}

	if r.Step != "" {
		breakdownFor(s.steps, r.Step).add(r)
	}

	if r.Status != 0 {
		h, ok := s.status[r.Status]
		if !ok {
			h = hdr.New()
			s.status[r.Status] = h
		}

		h.RecordDuration(r.Latency)
	}

	s.recordTimings(&r.Timings)
}

func (s *shard) recordTimings(t *Timings) {
	for i, d := range [...]time.Duration{t.DNS, t.Connect, t.TLS, t.TTFB, t.Download} {
		if d <= 0 {
			continue
		}

		if s.timings[i] == nil {
			s.timings[i] = hdr.New()
		}

		s.timings[i].RecordDuration(d)
	}
}

// sampleHeap keeps the N slowest results (a min-heap on latency).
type sampleHeap []Result

func (h sampleHeap) Len() int           { return len(h) }
func (h sampleHeap) Less(i, j int) bool { return h[i].Latency < h[j].Latency }
func (h sampleHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *sampleHeap) Push(x any) {
	r, _ := x.(Result)
	*h = append(*h, r)
}

func (h *sampleHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]

	return x
}

// Collector turns results into metrics. Record is safe for concurrent use;
// Tick, Metric and Snapshot may be called from other goroutines at any time.
type Collector struct {
	opts   CollectorOptions
	shards []*shard
	next   atomic.Uint64
	warm   atomic.Int64

	sampleMu    sync.Mutex
	slowest     sampleHeap
	firstFailed []Result

	mu           sync.Mutex
	series       []SecondBucket
	health       []HealthSample
	sampler      *healthSampler
	lastDropped  int64
	maxLag       time.Duration
	laggingSecs  int
	measuredSecs int
}

// NewCollector returns an empty collector.
func NewCollector(opts *CollectorOptions) *Collector {
	c := new(Collector)
	c.opts = *opts

	if c.opts.SampleN <= 0 {
		c.opts.SampleN = defaultSampleN
	}

	if c.opts.LagThreshold <= 0 {
		c.opts.LagThreshold = defaultLagThreshold
	}

	for range min(runtime.GOMAXPROCS(0), maxShards) {
		c.shards = append(c.shards, newShard())
	}

	c.sampler = newHealthSampler()

	return c
}

// Record adds one result. Warmup results are only counted.
func (c *Collector) Record(r *Result) {
	if r.Warmup {
		c.warm.Add(1)

		return
	}

	idx := c.next.Add(1) % uint64(len(c.shards))
	c.shards[idx].record(r)

	if r.Detail != nil {
		keepSample(c, r)
	}
}

func keepSample(c *Collector, r *Result) {
	c.sampleMu.Lock()
	defer c.sampleMu.Unlock()

	if r.ErrorClass != "" && len(c.firstFailed) < c.opts.SampleN {
		c.firstFailed = append(c.firstFailed, *r)
	}

	switch {
	case len(c.slowest) < c.opts.SampleN:
		heap.Push(&c.slowest, *r)
	case r.Latency > c.slowest[0].Latency:
		c.slowest[0] = *r
		heap.Fix(&c.slowest, 0)
	}
}

// Tick closes the current second: it merges what the shards recorded since
// the previous tick into a SecondBucket, adds the scheduler gauges and one
// generator health sample, and returns the bucket.
func (c *Collector) Tick() SecondBucket {
	merged := new(window)
	merged.byClass = make(map[ErrorClass]int64)
	merged.latency = hdr.New()

	for _, s := range c.shards {
		s.mu.Lock()
		merged.requests += s.win.requests
		merged.errors += s.win.errors
		merged.bytesIn += s.win.bytesIn
		merged.bytesOut += s.win.bytesOut
		merged.latency.Merge(s.win.latency)

		for k, v := range s.win.byClass {
			merged.byClass[k] += v
		}

		for i, n := range s.win.heat {
			merged.heat[i] += n
		}

		s.win.reset()
		s.mu.Unlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return closeSecond(c, merged)
}

func closeSecond(c *Collector, w *window) SecondBucket {
	var b SecondBucket

	b.Second = len(c.series) + 1
	b.Warmup = time.Duration(b.Second)*time.Second <= c.opts.Warmup
	b.Requests, b.Errors, b.BytesIn, b.BytesOut = w.requests, w.errors, w.bytesIn, w.bytesOut
	b.ErrorsByClass = w.byClass
	b.Heat = append([]uint32(nil), w.heat[:]...)
	b.P50 = float64(w.latency.ValueAtPercentile(percentP50)) / usPerMs
	b.P90 = float64(w.latency.ValueAtPercentile(percentP90)) / usPerMs
	b.P95 = float64(w.latency.ValueAtPercentile(percentP95)) / usPerMs
	b.P99 = float64(w.latency.ValueAtPercentile(percentP99)) / usPerMs
	b.Max = float64(w.latency.Max()) / usPerMs
	b.Mean = w.latency.Mean() / usPerMs

	h := c.sampler.sample(b.Second)

	if st := c.opts.Stats; st != nil {
		b.InFlight = st.InFlight()
		b.ActiveVUs = st.ActiveVUs()
		dropped := st.Dropped()
		b.Dropped = dropped - c.lastDropped
		c.lastDropped = dropped

		lag := st.TakeMaxLag()
		h.LagMs = float64(lag) / float64(time.Millisecond)

		if lag > c.opts.LagThreshold {
			c.laggingSecs++
		}

		c.maxLag = max(c.maxLag, lag)
	}

	if !b.Warmup {
		c.measuredSecs++
	}

	c.series = append(c.series, b)
	c.health = append(c.health, h)

	return b
}

// Run ticks every second until ctx ends, then closes the last partial second.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Tick()

			return
		case <-t.C:
			c.Tick()
		}
	}
}

// merged combines all shards for a scope: "" is everything, otherwise an
// operation or a step name.
func merged(c *Collector, scope string) *Breakdown {
	out := newBreakdown()

	for _, s := range c.shards {
		s.mu.Lock()

		switch {
		case scope == "":
			out.merge(s.overall)
		case s.ops[scope] != nil:
			out.merge(s.ops[scope])
		case s.steps[scope] != nil:
			out.merge(s.steps[scope])
		}

		s.mu.Unlock()
	}

	return out
}

// Metric returns the current value of a threshold metric (see
// domain.IsKnownMetric) for a scope, and false when there is no data yet.
// Latencies are in milliseconds, rates are fractions.
func (c *Collector) Metric(metric, scope string) (float64, bool) {
	switch metric {
	case domain.MetricDroppedCount:
		if c.opts.Stats == nil {
			return 0, false
		}

		return float64(c.opts.Stats.Dropped()), true
	case domain.MetricThroughputRPS:
		return throughput(c, scope)
	}

	b := merged(c, scope)
	if b.Totals.Requests == 0 {
		return 0, false
	}

	return metricOf(metric, b)
}

func metricOf(metric string, b *Breakdown) (float64, bool) {
	t := &b.Totals

	switch metric {
	case domain.MetricErrorsRate:
		return float64(t.Errors) / float64(t.Requests), true
	case domain.MetricErrorsCount:
		return float64(t.Errors), true
	case domain.MetricChecksRate:
		total := t.ChecksPassed + t.ChecksFailed
		if total == 0 {
			return 0, false
		}

		return float64(t.ChecksPassed) / float64(total), true
	case domain.MetricContractViolations:
		return float64(t.ContractViolations), true
	case domain.MetricLatencyAvg:
		return b.Latency.Mean() / usPerMs, true
	case domain.MetricLatencyMin:
		return float64(b.Latency.Min()) / usPerMs, true
	case domain.MetricLatencyMax:
		return float64(b.Latency.Max()) / usPerMs, true
	default:
		return latencyPercentile(metric, b.Latency)
	}
}

func latencyPercentile(metric string, h *hdr.Histogram) (float64, bool) {
	p, err := strconv.ParseFloat(strings.TrimPrefix(metric, latencyPercentilePrefix), 64)
	if err != nil || !strings.HasPrefix(metric, latencyPercentilePrefix) {
		return 0, false
	}

	return float64(h.ValueAtPercentile(p)) / usPerMs, true
}

func throughput(c *Collector, scope string) (float64, bool) {
	c.mu.Lock()
	secs := c.measuredSecs
	c.mu.Unlock()

	b := merged(c, scope)
	if secs == 0 || b.Totals.Requests == 0 {
		return 0, false
	}

	return float64(b.Totals.Requests) / float64(secs), true
}

// Summary is the whole run as measured.
type Summary struct {
	Overall    *Breakdown
	Operations map[string]*Breakdown
	Steps      map[string]*Breakdown
	// Status holds latency per HTTP status code.
	Status map[int]*hdr.Histogram
	// Timings holds latency per phase (see the Phase constants).
	Timings map[string]*hdr.Histogram
	Series  []SecondBucket
	Health  []HealthSample
	// Slowest is sorted slowest first; FirstFailed is in arrival order.
	Slowest     []Result
	FirstFailed []Result
	// Dropped counts open-model iterations skipped at maxInFlight.
	Dropped int64
	// WarmupRequests were left out of every figure above.
	WarmupRequests int64
	// MeasuredSeconds is the number of seconds after warmup.
	MeasuredSeconds int
	// Untrustworthy is set when the generator could not keep up with the plan.
	Untrustworthy       bool
	UntrustworthyReason string
	MaxLag              time.Duration
}

// Snapshot merges everything recorded so far. It can be called while the run
// is going on.
func (c *Collector) Snapshot() *Summary {
	s := new(Summary)
	s.Overall = newBreakdown()
	s.Operations = make(map[string]*Breakdown)
	s.Steps = make(map[string]*Breakdown)
	s.Status = make(map[int]*hdr.Histogram)
	s.Timings = make(map[string]*hdr.Histogram)

	for _, sh := range c.shards {
		sh.mu.Lock()
		s.Overall.merge(sh.overall)

		for k, b := range sh.ops {
			breakdownFor(s.Operations, k).merge(b)
		}

		for k, b := range sh.steps {
			breakdownFor(s.Steps, k).merge(b)
		}

		for code, h := range sh.status {
			if s.Status[code] == nil {
				s.Status[code] = hdr.New()
			}

			s.Status[code].Merge(h)
		}

		for i, h := range sh.timings {
			if h == nil {
				continue
			}

			if s.Timings[phases[i]] == nil {
				s.Timings[phases[i]] = hdr.New()
			}

			s.Timings[phases[i]].Merge(h)
		}

		sh.mu.Unlock()
	}

	c.fillRunInfo(s)

	return s
}

func (c *Collector) fillRunInfo(s *Summary) {
	s.WarmupRequests = c.warm.Load()

	c.mu.Lock()
	s.Series = append([]SecondBucket(nil), c.series...)
	s.Health = append([]HealthSample(nil), c.health...)
	s.MeasuredSeconds = c.measuredSecs
	s.MaxLag = c.maxLag

	if c.laggingSecs > 0 {
		s.Untrustworthy = true
		s.UntrustworthyReason = "the load generator lagged the plan by more than " +
			c.opts.LagThreshold.String() + " in " + strconv.Itoa(c.laggingSecs) + " s; results may be too optimistic"
	}

	c.mu.Unlock()

	if c.opts.Stats != nil {
		s.Dropped = c.opts.Stats.Dropped()
	}

	c.sampleMu.Lock()
	s.FirstFailed = append([]Result(nil), c.firstFailed...)
	s.Slowest = append([]Result(nil), c.slowest...)
	c.sampleMu.Unlock()

	// Slowest first.
	for i := range s.Slowest {
		for j := i + 1; j < len(s.Slowest); j++ {
			if s.Slowest[j].Latency > s.Slowest[i].Latency {
				s.Slowest[i], s.Slowest[j] = s.Slowest[j], s.Slowest[i]
			}
		}
	}
}
