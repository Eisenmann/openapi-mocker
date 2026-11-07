package usecase

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WritePrometheus renders the metrics in the Prometheus text exposition
// format (version 0.0.4).
func (h *LogHub) WritePrometheus(w io.Writer) error {
	h.mu.Lock()
	snap := h.obs.snapshot()
	subs := len(h.subs)
	h.mu.Unlock()

	exports, queued := h.exportStats()

	bw := bufio.NewWriter(w)

	snap.write(bw)

	metricHeader(bw, "openapimocker_log_stream_subscribers", "gauge", "Open live log streams.")
	fmt.Fprintf(bw, "openapimocker_log_stream_subscribers %d\n", subs)

	writeExportMetrics(bw, exports, queued, h.exporter != nil)

	return bw.Flush()
}

func (h *LogHub) exportStats() (stats []ExporterStats, queued int) {
	if h.exporter == nil {
		return nil, 0
	}

	return h.exporter.Stats()
}

func writeExportMetrics(w *bufio.Writer, stats []ExporterStats, queued int, enabled bool) {
	if !enabled {
		return
	}

	const name = "openapimocker_log_export_events_total"

	metricHeader(w, name, "counter", "Log entries handed to an export sink, by outcome.")

	for _, s := range stats {
		fmt.Fprintf(w, "%s{sink=%s,result=\"sent\"} %d\n", name, quote(s.Sink), s.Sent)
		fmt.Fprintf(w, "%s{sink=%s,result=\"failed\"} %d\n", name, quote(s.Sink), s.Failed)
		fmt.Fprintf(w, "%s{sink=%s,result=\"dropped\"} %d\n", name, quote(s.Sink), s.Dropped)
	}

	metricHeader(w, "openapimocker_log_export_queue_length", "gauge", "Log entries waiting to be exported.")
	fmt.Fprintf(w, "openapimocker_log_export_queue_length %d\n", queued)
}

// metricsSnapshot is a copy of the counters, taken under the hub's lock.
type metricsSnapshot struct {
	started       float64
	requests      map[codeKey]uint64
	endpoints     map[endpointKey]uint64
	durations     map[string]histogram
	faults        map[faultKey]uint64
	violations    map[string]uint64
	droppedSeries uint64
}

func (o *observations) snapshot() *metricsSnapshot {
	s := &metricsSnapshot{
		started:  float64(o.started.UnixNano()) / float64(time.Second),
		requests: make(map[codeKey]uint64, len(o.requests)), endpoints: make(map[endpointKey]uint64, len(o.endpoints)),
		durations: make(map[string]histogram, len(o.durations)), faults: make(map[faultKey]uint64, len(o.faults)),
		violations: make(map[string]uint64, len(o.violations)), droppedSeries: o.droppedSeries,
	}

	for k, v := range o.requests {
		s.requests[k] = v
	}

	for k, v := range o.endpoints {
		s.endpoints[k] = v.hits
	}

	for k, v := range o.durations {
		s.durations[k] = histogram{buckets: append([]uint64(nil), v.buckets...), count: v.count, sum: v.sum}
	}

	for k, v := range o.faults {
		s.faults[k] = v
	}

	for k, v := range o.violations {
		s.violations[k] = v
	}

	return s
}

func (s *metricsSnapshot) write(w *bufio.Writer) {
	metricHeader(w, "openapimocker_start_time_seconds", "gauge", "Unix time the server started.")
	fmt.Fprintf(w, "openapimocker_start_time_seconds %s\n", formatFloat(s.started))

	s.writeRequests(w)
	s.writeEndpoints(w)
	s.writeDurations(w)
	s.writeFaults(w)

	metricHeader(w, "openapimocker_request_violations_total", "counter",
		"Request-validation problems found, by project.")

	for _, p := range sortedKeys(s.violations) {
		fmt.Fprintf(w, "openapimocker_request_violations_total{project=%s} %d\n", quote(p), s.violations[p])
	}

	metricHeader(w, "openapimocker_dropped_series_total", "counter",
		"Observations not recorded because a series limit was reached.")
	fmt.Fprintf(w, "openapimocker_dropped_series_total %d\n", s.droppedSeries)
}

func (s *metricsSnapshot) writeRequests(w *bufio.Writer) {
	metricHeader(w, "openapimocker_requests_total", "counter", "Mock requests served.")

	keys := make([]codeKey, 0, len(s.requests))
	for k := range s.requests {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.project != b.project {
			return a.project < b.project
		}

		if a.method != b.method {
			return a.method < b.method
		}

		return a.code < b.code
	})

	for _, k := range keys {
		fmt.Fprintf(w, "openapimocker_requests_total{project=%s,method=%s,code=%s} %d\n",
			quote(k.project), quote(k.method), quote(strconv.Itoa(k.code)), s.requests[k])
	}
}

func (s *metricsSnapshot) writeEndpoints(w *bufio.Writer) {
	metricHeader(w, "openapimocker_endpoint_hits_total", "counter", "Mock requests per contract operation.")

	keys := make([]endpointKey, 0, len(s.endpoints))
	for k := range s.endpoints {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.project != b.project {
			return a.project < b.project
		}

		if a.endpoint != b.endpoint {
			return a.endpoint < b.endpoint
		}

		return a.method < b.method
	})

	for _, k := range keys {
		fmt.Fprintf(w, "openapimocker_endpoint_hits_total{project=%s,method=%s,endpoint=%s} %d\n",
			quote(k.project), quote(k.method), quote(k.endpoint), s.endpoints[k])
	}
}

func (s *metricsSnapshot) writeDurations(w *bufio.Writer) {
	const name = "openapimocker_request_duration_seconds"

	metricHeader(w, name, "histogram", "Time to answer a mock request, including injected latency.")

	for _, p := range sortedKeys(s.durations) {
		h := s.durations[p]
		for i, upper := range durationBuckets {
			fmt.Fprintf(w, "%s_bucket{project=%s,le=%s} %d\n", name, quote(p), quote(formatFloat(upper)), h.buckets[i])
		}

		fmt.Fprintf(w, "%s_bucket{project=%s,le=\"+Inf\"} %d\n", name, quote(p), h.count)
		fmt.Fprintf(w, "%s_sum{project=%s} %s\n", name, quote(p), formatFloat(h.sum))
		fmt.Fprintf(w, "%s_count{project=%s} %d\n", name, quote(p), h.count)
	}
}

func (s *metricsSnapshot) writeFaults(w *bufio.Writer) {
	metricHeader(w, "openapimocker_faults_total", "counter", "Injected faults, by project and kind.")

	keys := make([]faultKey, 0, len(s.faults))
	for k := range s.faults {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].project != keys[j].project {
			return keys[i].project < keys[j].project
		}

		return keys[i].fault < keys[j].fault
	})

	for _, k := range keys {
		fmt.Fprintf(w, "openapimocker_faults_total{project=%s,fault=%s} %d\n",
			quote(k.project), quote(k.fault), s.faults[k])
	}
}

func metricHeader(w io.Writer, name, kind, help string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`) //nolint:gochecknoglobals // immutable.

// quote renders a label value.
func quote(v string) string { return `"` + labelEscaper.Replace(v) + `"` }

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
