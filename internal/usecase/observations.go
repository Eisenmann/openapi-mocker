package usecase

import (
	"net/http"
	"sort"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	// unmatchedEndpoint labels requests that no contract operation answered.
	unmatchedEndpoint = "unmatched"
	// otherMethod labels HTTP methods outside the standard set.
	otherMethod = "OTHER"

	// maxSeriesPerFamily bounds the label combinations of one metric family,
	// so request paths chosen by a client can never grow memory without limit.
	maxSeriesPerFamily = 5000
	// maxStatEndpoints bounds the endpoints tracked per project.
	maxStatEndpoints = 1000
	// maxStatProjects bounds the projects tracked.
	maxStatProjects = 500
)

// durationBuckets are the upper bounds (seconds) of the latency histogram.
var durationBuckets = []float64{ //nolint:gochecknoglobals // a constant table.
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// knownMethods are the HTTP methods reported under their own name.
var knownMethods = map[string]bool{ //nolint:gochecknoglobals // a constant table.
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
	http.MethodDelete: true, http.MethodHead: true, http.MethodOptions: true,
}

type endpointKey struct {
	project  string
	method   string
	endpoint string
}

type codeKey struct {
	project string
	method  string
	code    int
}

type faultKey struct {
	project string
	fault   string
}

type histogram struct {
	buckets []uint64
	count   uint64
	sum     float64
}

// endpointStat is the running figure of one operation.
type endpointStat struct {
	hits    uint64
	errors  uint64
	totalMs int64
	maxMs   int64
	last    time.Time
}

// observations holds the counters behind /metrics and the stats endpoint.
// It is guarded by the owning LogHub's mutex.
type observations struct {
	started time.Time

	requests   map[codeKey]uint64
	endpoints  map[endpointKey]*endpointStat
	durations  map[string]*histogram
	faults     map[faultKey]uint64
	violations map[string]uint64
	statSince  map[string]time.Time

	droppedSeries uint64
	projects      map[string]int
}

func newObservations() *observations {
	return &observations{
		started:  time.Time{},
		requests: map[codeKey]uint64{}, endpoints: map[endpointKey]*endpointStat{},
		durations: map[string]*histogram{}, faults: map[faultKey]uint64{},
		violations: map[string]uint64{}, statSince: map[string]time.Time{},
		droppedSeries: 0, projects: map[string]int{},
	}
}

func normalizeMethod(m string) string {
	if knownMethods[m] {
		return m
	}

	return otherMethod
}

func (o *observations) record(l *domain.RequestLog) {
	method := normalizeMethod(l.Method)

	endpoint := l.Endpoint
	if endpoint == "" {
		endpoint = unmatchedEndpoint
	}

	ck := codeKey{project: l.ProjectID, method: method, code: l.StatusCode}
	if _, seen := o.requests[ck]; seen || len(o.requests) < maxSeriesPerFamily {
		o.requests[ck]++
	} else {
		o.droppedSeries++
	}

	o.recordEndpoint(l, method, endpoint)
	o.recordDuration(l)

	if l.Fault != "" {
		fk := faultKey{project: l.ProjectID, fault: l.Fault}
		if _, seen := o.faults[fk]; seen || len(o.faults) < maxSeriesPerFamily {
			o.faults[fk]++
		}
	}

	if n := len(l.Violations); n > 0 {
		if _, seen := o.violations[l.ProjectID]; seen || len(o.violations) < maxSeriesPerFamily {
			o.violations[l.ProjectID] += uint64(n)
		}
	}
}

func (o *observations) recordEndpoint(l *domain.RequestLog, method, endpoint string) {
	if _, known := o.statSince[l.ProjectID]; !known {
		if len(o.statSince) >= maxStatProjects {
			o.droppedSeries++
			return
		}

		o.statSince[l.ProjectID] = l.Timestamp
	}

	key := endpointKey{project: l.ProjectID, method: method, endpoint: endpoint}

	st := o.endpoints[key]
	if st == nil {
		if o.projects[l.ProjectID] >= maxStatEndpoints {
			o.droppedSeries++
			return
		}

		st = &endpointStat{hits: 0, errors: 0, totalMs: 0, maxMs: 0, last: time.Time{}}
		o.endpoints[key] = st
		o.projects[l.ProjectID]++
	}

	st.hits++
	st.totalMs += l.DurationMs
	st.maxMs = max(st.maxMs, l.DurationMs)
	st.last = l.Timestamp

	if l.StatusCode >= http.StatusBadRequest {
		st.errors++
	}
}

func (o *observations) recordDuration(l *domain.RequestLog) {
	h := o.durations[l.ProjectID]
	if h == nil {
		if len(o.durations) >= maxSeriesPerFamily {
			return
		}

		h = &histogram{buckets: make([]uint64, len(durationBuckets)), count: 0, sum: 0}
		o.durations[l.ProjectID] = h
	}

	sec := (time.Duration(l.DurationMs) * time.Millisecond).Seconds()
	h.count++
	h.sum += sec

	for i, upper := range durationBuckets {
		if sec <= upper {
			h.buckets[i]++
		}
	}
}

// EndpointStat is the hit figure of one operation of a project.
type EndpointStat struct {
	Method    string    `json:"method"`
	Endpoint  string    `json:"endpoint"`
	Hits      uint64    `json:"hits"`
	Errors    uint64    `json:"errors"`
	AvgMs     float64   `json:"avgMs"`
	MaxMs     int64     `json:"maxMs"`
	LastHitAt time.Time `json:"lastHitAt"`
}

// ProjectStats is what the stats endpoint returns.
type ProjectStats struct {
	// Since is when counting started (the first request seen, or the last reset).
	Since     time.Time      `json:"since"`
	Total     uint64         `json:"total"`
	Errors    uint64         `json:"errors"`
	Endpoints []EndpointStat `json:"endpoints"`
}

// Stats returns the per-endpoint hit counts of a project, busiest first.
// Counting starts when the server starts and is not persisted.
func (h *LogHub) Stats(projectID string) ProjectStats {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := ProjectStats{
		Since: h.obs.statSince[projectID], Total: 0, Errors: 0, Endpoints: []EndpointStat{},
	}
	if out.Since.IsZero() {
		out.Since = h.obs.started
	}

	for k, st := range h.obs.endpoints {
		if k.project != projectID {
			continue
		}

		out.Total += st.hits
		out.Errors += st.errors
		out.Endpoints = append(out.Endpoints, EndpointStat{
			Method: k.method, Endpoint: k.endpoint, Hits: st.hits, Errors: st.errors,
			AvgMs: float64(st.totalMs) / float64(st.hits), MaxMs: st.maxMs, LastHitAt: st.last,
		})
	}

	sort.Slice(out.Endpoints, func(i, j int) bool {
		a, b := &out.Endpoints[i], &out.Endpoints[j]
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}

		if a.Endpoint != b.Endpoint {
			return a.Endpoint < b.Endpoint
		}

		return a.Method < b.Method
	})

	return out
}

// ResetStats clears the project's hit counts and its metric series.
func (h *LogHub) ResetStats(projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	o := h.obs

	for k := range o.endpoints {
		if k.project == projectID {
			delete(o.endpoints, k)
		}
	}

	for k := range o.requests {
		if k.project == projectID {
			delete(o.requests, k)
		}
	}

	for k := range o.faults {
		if k.project == projectID {
			delete(o.faults, k)
		}
	}

	delete(o.durations, projectID)
	delete(o.violations, projectID)
	delete(o.statSince, projectID)
	delete(o.projects, projectID)
}
