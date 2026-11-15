// Package report turns the outcome of a load run into documents: the JSON
// report that other formats are built from, and JUnit XML for CI.
package report

import (
	"math"
	"runtime"
	"sort"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/hdr"
)

// SchemaVersion is the version of the JSON report layout. It is bumped when a
// field changes meaning or is removed; added fields do not change it.
const SchemaVersion = 1

const (
	usPerMs       = 1000.0
	roundFactor   = 1000.0
	percentP50    = 50
	percentP75    = 75
	percentP90    = 90
	percentP95    = 95
	percentP99    = 99
	percentP999   = 99.9
	msPerSecond   = 1000.0
	histogramBins = 40
)

// Environment describes the machine and build that produced the numbers.
type Environment struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPUs      int    `json:"cpus"`
}

// CurrentEnvironment describes this process; version is the build version.
func CurrentEnvironment(version string) Environment {
	return Environment{
		Version: version, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
	}
}

// Input is what a report is built from.
type Input struct {
	Run         *domain.LoadRun
	Summary     *loadtest.Summary
	Environment Environment
}

// Report is the machine-readable result of a run; see docs/load-report.schema.json.
// All latencies are in milliseconds.
type Report struct {
	SchemaVersion int              `json:"schemaVersion"`
	Run           RunInfo          `json:"run"`
	Thresholds    []Threshold      `json:"thresholds"`
	Totals        Totals           `json:"totals"`
	Latency       Latency          `json:"latency"`
	Errors        map[string]int64 `json:"errors"`
	Operations    []Slice          `json:"operations"`
	Steps         []Slice          `json:"steps"`
	Status        []StatusStats    `json:"status"`
	Timings       []PhaseStats     `json:"timings"`
	Histogram     []HistogramBin   `json:"histogram"`
	HeatUpperMs   []float64        `json:"heatUpperMs"`
	Series        []Point          `json:"series"`
	Health        []Health         `json:"health"`
	Generator     Generator        `json:"generator"`
	Slowest       []Sample         `json:"slowest"`
	FirstFailed   []Sample         `json:"firstFailed"`
	Environment   Environment      `json:"environment"`
	Plan          domain.Plan      `json:"plan"`
}

// RunInfo identifies the run.
type RunInfo struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Target          string     `json:"target"`
	State           string     `json:"state"`
	Verdict         string     `json:"verdict,omitempty"`
	AbortReason     string     `json:"abortReason,omitempty"`
	Error           string     `json:"error,omitempty"`
	Actor           string     `json:"actor,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	DurationSeconds float64    `json:"durationSeconds"`
}

// Threshold is one threshold with what was measured.
type Threshold struct {
	Metric      string   `json:"metric"`
	Scope       string   `json:"scope,omitempty"`
	Op          string   `json:"op"`
	Value       float64  `json:"value"`
	Observed    *float64 `json:"observed,omitempty"`
	Passed      bool     `json:"passed"`
	AbortOnFail bool     `json:"abortOnFail,omitempty"`
}

// Totals are the headline counters of the measured period (warmup excluded).
type Totals struct {
	Requests           int64   `json:"requests"`
	Errors             int64   `json:"errors"`
	ErrorRate          float64 `json:"errorRate"`
	ChecksPassed       int64   `json:"checksPassed"`
	ChecksFailed       int64   `json:"checksFailed"`
	ContractViolations int64   `json:"contractViolations"`
	BytesIn            int64   `json:"bytesIn"`
	BytesOut           int64   `json:"bytesOut"`
	ThroughputRPS      float64 `json:"throughputRps"`
	Dropped            int64   `json:"dropped"`
	WarmupRequests     int64   `json:"warmupRequests"`
	MeasuredSeconds    int     `json:"measuredSeconds"`
}

// Latency is a latency distribution.
type Latency struct {
	Min    float64 `json:"min"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stdDev"`
	P50    float64 `json:"p50"`
	P75    float64 `json:"p75"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	P999   float64 `json:"p999"`
	Max    float64 `json:"max"`
}

// Slice is the figures of one operation or one step.
type Slice struct {
	Name               string           `json:"name"`
	Requests           int64            `json:"requests"`
	Errors             int64            `json:"errors"`
	ErrorRate          float64          `json:"errorRate"`
	ContractViolations int64            `json:"contractViolations"`
	ChecksFailed       int64            `json:"checksFailed"`
	ErrorsByClass      map[string]int64 `json:"errorsByClass,omitempty"`
	Latency            Latency          `json:"latency"`
}

// StatusStats is the latency of one HTTP status code.
type StatusStats struct {
	Code    int     `json:"code"`
	Count   int64   `json:"count"`
	Latency Latency `json:"latency"`
}

// PhaseStats is one phase of the request timing breakdown.
type PhaseStats struct {
	Phase   string  `json:"phase"`
	Count   int64   `json:"count"`
	Latency Latency `json:"latency"`
}

// HistogramBin is the number of requests whose latency fell below UpperMs and
// above the previous bin's bound.
type HistogramBin struct {
	UpperMs float64 `json:"upperMs"`
	Count   int64   `json:"count"`
}

// Point is one second of the run.
type Point struct {
	Second        int              `json:"second"`
	Warmup        bool             `json:"warmup,omitempty"`
	Requests      int64            `json:"requests"`
	Errors        int64            `json:"errors"`
	ErrorsByClass map[string]int64 `json:"errorsByClass,omitempty"`
	InFlight      int64            `json:"inFlight"`
	VUs           int64            `json:"vus,omitempty"`
	Dropped       int64            `json:"dropped,omitempty"`
	P50           float64          `json:"p50"`
	P90           float64          `json:"p90"`
	P95           float64          `json:"p95"`
	P99           float64          `json:"p99"`
	Max           float64          `json:"max"`
	Mean          float64          `json:"mean"`
	// Heat counts requests per latency bin (bounds in Report.HeatUpperMs).
	Heat []uint32 `json:"heat,omitempty"`
}

// Health is the load generator's own state during one second.
type Health struct {
	Second            int     `json:"second"`
	CPUCores          float64 `json:"cpuCores"`
	Goroutines        int     `json:"goroutines"`
	GCPauseMaxMs      float64 `json:"gcPauseMaxMs"`
	SchedLatencyP99Ms float64 `json:"schedLatencyP99Ms"`
	LagMs             float64 `json:"lagMs"`
}

// Generator says whether the load generator kept up with the plan.
type Generator struct {
	Untrustworthy bool    `json:"untrustworthy"`
	Reason        string  `json:"reason,omitempty"`
	MaxLagMs      float64 `json:"maxLagMs"`
}

// Sample is a slow or failed request kept with its detail.
type Sample struct {
	Step         string  `json:"step"`
	Operation    string  `json:"operation"`
	Status       int     `json:"status"`
	LatencyMs    float64 `json:"latencyMs"`
	ErrorClass   string  `json:"errorClass,omitempty"`
	ChecksFailed int     `json:"checksFailed,omitempty"`
	Violations   int     `json:"contractViolations,omitempty"`
	Detail       *Detail `json:"detail,omitempty"`
}

// Detail is the redacted request and response of a sample.
type Detail struct {
	Method          string              `json:"method"`
	URL             string              `json:"url"`
	RequestHeaders  map[string][]string `json:"requestHeaders,omitempty"`
	RequestBody     string              `json:"requestBody,omitempty"`
	ResponseHeaders map[string][]string `json:"responseHeaders,omitempty"`
	ResponseBody    string              `json:"responseBody,omitempty"`
	Error           string              `json:"error,omitempty"`
}

// Build assembles the report of a run.
func Build(in *Input) *Report {
	r := new(Report)
	r.SchemaVersion = SchemaVersion
	r.Environment = in.Environment
	r.Plan = in.Run.Plan
	r.Run = runInfo(in.Run)
	r.Thresholds = thresholds(in.Run.Thresholds)

	s := in.Summary
	if s == nil {
		s = emptySummary()
	}

	fillTotals(r, s)
	fillSlices(r, s)
	fillSeries(r, s)
	fillSamples(r, s)

	return r
}

func emptySummary() *loadtest.Summary {
	s := new(loadtest.Summary)
	s.Overall = new(loadtest.Breakdown)
	s.Overall.Latency = hdr.New()

	return s
}

func runInfo(run *domain.LoadRun) RunInfo {
	var ri RunInfo

	ri.ID = run.ID
	ri.Name = run.Plan.Name
	ri.Target = run.Target
	ri.State = string(run.State)
	ri.Verdict = string(run.Verdict)
	ri.AbortReason = run.AbortReason
	ri.Error = run.Error
	ri.Actor = run.Actor
	ri.StartedAt = run.StartedAt
	ri.FinishedAt = run.FinishedAt

	if run.StartedAt != nil && run.FinishedAt != nil {
		ri.DurationSeconds = round(run.FinishedAt.Sub(*run.StartedAt).Seconds())
	}

	return ri
}

func thresholds(in []domain.LoadThresholdResult) []Threshold {
	out := make([]Threshold, 0, len(in))

	for i := range in {
		t := &in[i]

		var e Threshold

		e.Metric = t.Threshold.Metric
		e.Scope = t.Threshold.Scope
		e.Op = string(t.Threshold.Op)
		e.Value = t.Threshold.Value
		e.Passed = t.Passed
		e.AbortOnFail = t.Threshold.AbortOnFail

		if t.HasData {
			v := round(t.Observed)
			e.Observed = &v
		}

		out = append(out, e)
	}

	return out
}

func fillTotals(r *Report, s *loadtest.Summary) {
	t := &s.Overall.Totals

	r.Totals.Requests = t.Requests
	r.Totals.Errors = t.Errors
	r.Totals.ChecksPassed = t.ChecksPassed
	r.Totals.ChecksFailed = t.ChecksFailed
	r.Totals.ContractViolations = t.ContractViolations
	r.Totals.BytesIn = t.BytesIn
	r.Totals.BytesOut = t.BytesOut
	r.Totals.Dropped = s.Dropped
	r.Totals.WarmupRequests = s.WarmupRequests
	r.Totals.MeasuredSeconds = s.MeasuredSeconds

	if t.Requests > 0 {
		r.Totals.ErrorRate = round(float64(t.Errors) / float64(t.Requests))
	}

	if s.MeasuredSeconds > 0 {
		r.Totals.ThroughputRPS = round(float64(t.Requests) / float64(s.MeasuredSeconds))
	}

	r.Latency = latencyOf(s.Overall.Latency)
	r.Errors = classes(t.ByClass)
	r.Histogram = histogramOf(s.Overall.Latency)
	r.Generator = Generator{Untrustworthy: s.Untrustworthy, Reason: s.UntrustworthyReason, MaxLagMs: round(msOf(s.MaxLag))}
}

func fillSlices(r *Report, s *loadtest.Summary) {
	r.Operations = slices(s.Operations)
	r.Steps = slices(s.Steps)
	r.Status = statuses(s.Status)
	r.Timings = phases(s.Timings)
}

func fillSeries(r *Report, s *loadtest.Summary) {
	r.Series = make([]Point, 0, len(s.Series))
	r.Health = make([]Health, 0, len(s.Health))

	for i := range s.Series {
		r.Series = append(r.Series, point(&s.Series[i]))
	}

	for _, h := range s.Health {
		r.Health = append(r.Health, Health{
			Second: h.Second, CPUCores: round(h.CPUCores), Goroutines: h.Goroutines, GCPauseMaxMs: round(h.GCPauseMaxMs),
			SchedLatencyP99Ms: round(h.SchedLatencyP99Ms), LagMs: round(h.LagMs),
		})
	}

	r.HeatUpperMs = make([]float64, histogramBins)
	for i := range r.HeatUpperMs {
		r.HeatUpperMs[i] = round(loadtest.HeatBinUpperMs(i))
	}
}

func fillSamples(r *Report, s *loadtest.Summary) {
	r.Slowest = samples(s.Slowest)
	r.FirstFailed = samples(s.FirstFailed)
}

func point(b *loadtest.SecondBucket) Point {
	var p Point

	p.Second = b.Second
	p.Warmup = b.Warmup
	p.Requests = b.Requests
	p.Errors = b.Errors
	p.ErrorsByClass = classes(b.ErrorsByClass)
	p.InFlight = b.InFlight
	p.VUs = b.ActiveVUs
	p.Dropped = b.Dropped
	p.P50, p.P90, p.P95, p.P99 = round(b.P50), round(b.P90), round(b.P95), round(b.P99)
	p.Max, p.Mean = round(b.Max), round(b.Mean)
	p.Heat = b.Heat

	if len(p.ErrorsByClass) == 0 {
		p.ErrorsByClass = nil
	}

	return p
}

func latencyOf(h *hdr.Histogram) Latency {
	var none Latency

	if h == nil || h.Count() == 0 {
		return none
	}

	at := func(p float64) float64 { return round(float64(h.ValueAtPercentile(p)) / usPerMs) }

	return Latency{
		Min: round(float64(h.Min()) / usPerMs), Mean: round(h.Mean() / usPerMs), StdDev: round(h.StdDev() / usPerMs),
		P50: at(percentP50), P75: at(percentP75), P90: at(percentP90), P95: at(percentP95), P99: at(percentP99),
		P999: at(percentP999), Max: round(float64(h.Max()) / usPerMs),
	}
}

// histogramOf groups the latency histogram into the heatmap's log-scale bins.
func histogramOf(h *hdr.Histogram) []HistogramBin {
	bins := make([]HistogramBin, histogramBins)
	for i := range bins {
		bins[i].UpperMs = round(loadtest.HeatBinUpperMs(i))
	}

	if h == nil {
		return bins
	}

	h.Iterate(func(b hdr.Bucket) bool {
		ms := float64(b.Value) / usPerMs
		i := sort.Search(histogramBins, func(i int) bool { return ms <= loadtest.HeatBinUpperMs(i) })
		bins[min(i, histogramBins-1)].Count += b.Count

		return true
	})

	return bins
}

func slices(m map[string]*loadtest.Breakdown) []Slice {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}

	sort.Strings(names)

	out := make([]Slice, 0, len(names))

	for _, name := range names {
		b := m[name]

		var sl Slice

		sl.Name = name
		sl.Requests = b.Totals.Requests
		sl.Errors = b.Totals.Errors
		sl.ContractViolations = b.Totals.ContractViolations
		sl.ChecksFailed = b.Totals.ChecksFailed
		sl.ErrorsByClass = classes(b.Totals.ByClass)
		sl.Latency = latencyOf(b.Latency)

		if sl.Requests > 0 {
			sl.ErrorRate = round(float64(sl.Errors) / float64(sl.Requests))
		}

		out = append(out, sl)
	}

	return out
}

func statuses(m map[int]*hdr.Histogram) []StatusStats {
	codes := make([]int, 0, len(m))
	for c := range m {
		codes = append(codes, c)
	}

	sort.Ints(codes)

	out := make([]StatusStats, 0, len(codes))
	for _, c := range codes {
		out = append(out, StatusStats{Code: c, Count: m[c].Count(), Latency: latencyOf(m[c])})
	}

	return out
}

func phases(m map[string]*hdr.Histogram) []PhaseStats {
	order := []string{
		loadtest.PhaseDNS, loadtest.PhaseConnect, loadtest.PhaseTLS, loadtest.PhaseTTFB, loadtest.PhaseDownload,
	}

	out := make([]PhaseStats, 0, len(order))

	for _, name := range order {
		if h, ok := m[name]; ok && h.Count() > 0 {
			out = append(out, PhaseStats{Phase: name, Count: h.Count(), Latency: latencyOf(h)})
		}
	}

	return out
}

func classes(m map[loadtest.ErrorClass]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[string(k)] = v
	}

	return out
}

func samples(in []loadtest.Result) []Sample {
	out := make([]Sample, 0, len(in))

	for i := range in {
		res := &in[i]

		var s Sample

		s.Step = res.Step
		s.Operation = res.Operation
		s.Status = res.Status
		s.LatencyMs = round(msOf(res.Latency))
		s.ErrorClass = string(res.ErrorClass)
		s.ChecksFailed = res.ChecksFailed
		s.Violations = res.ContractViolations

		if d := res.Detail; d != nil {
			s.Detail = &Detail{
				Method: d.Method, URL: d.URL, RequestHeaders: d.RequestHeaders, RequestBody: d.RequestBody,
				ResponseHeaders: d.ResponseHeaders, ResponseBody: d.ResponseBody, Error: d.Error,
			}
		}

		out = append(out, s)
	}

	return out
}

func msOf(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// round keeps three decimals, so reports are compact and stable.
func round(v float64) float64 { return math.Round(v*roundFactor) / roundFactor }
