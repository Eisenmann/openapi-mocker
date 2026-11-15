package report_test

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/hdr"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func hist(valuesMs ...float64) *hdr.Histogram {
	h := hdr.New()
	for _, v := range valuesMs {
		h.RecordDuration(time.Duration(v * float64(time.Millisecond)))
	}

	return h
}

func breakdown(requests, errors int64, valuesMs ...float64) *loadtest.Breakdown {
	b := new(loadtest.Breakdown)
	b.Latency = hist(valuesMs...)
	b.Totals.Requests = requests
	b.Totals.Errors = errors
	b.Totals.ChecksPassed = requests - errors
	b.Totals.ChecksFailed = errors
	b.Totals.BytesIn = requests * 100
	b.Totals.ByClass = map[loadtest.ErrorClass]int64{}

	if errors > 0 {
		b.Totals.ByClass[loadtest.ClassHTTP5xx] = errors
		b.Totals.ByClass[loadtest.ClassCheckFailed] = errors
	}

	return b
}

func fixture() *report.Input {
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ended := started.Add(12 * time.Second)

	run := new(domain.LoadRun)
	run.ID = "run_1"
	run.ProjectID = "p1"
	run.Target = "http://127.0.0.1:8080/mock/p1"
	run.Actor = "ann"
	run.State = domain.LoadRunFinished
	run.Verdict = domain.VerdictFail
	run.StartedAt = &started
	run.FinishedAt = &ended
	run.Plan.Name = "pets <load> & test"
	run.Plan.Target.BaseURL = "mock://p1"
	run.Thresholds = []domain.LoadThresholdResult{
		{Threshold: domain.Threshold{Metric: "http.latency.p95", Op: domain.OpLess, Value: 500}, Observed: 120.5, HasData: true, Passed: true},
		{Threshold: domain.Threshold{Metric: "errors.rate", Op: domain.OpLess, Value: 0.01, Scope: "listPets"}, Observed: 0.2, HasData: true},
		{Threshold: domain.Threshold{Metric: "http.latency.p99", Op: domain.OpLess, Value: 900, Scope: "ghost"}},
	}

	s := new(loadtest.Summary)
	s.Overall = breakdown(10, 2, 5, 8, 10, 12, 15, 20, 40, 80, 120, 300)
	s.Operations = map[string]*loadtest.Breakdown{
		"listPets": breakdown(6, 2, 5, 8, 10, 12, 15, 20),
		"getPet":   breakdown(4, 0, 40, 80, 120, 300),
	}
	s.Steps = map[string]*loadtest.Breakdown{"read/listPets": breakdown(6, 2, 5, 8, 10, 12, 15, 20)}
	s.Status = map[int]*hdr.Histogram{200: hist(5, 8, 10), 500: hist(300, 120)}
	s.Timings = map[string]*hdr.Histogram{loadtest.PhaseTTFB: hist(2, 3, 4), loadtest.PhaseDownload: hist(1, 1, 1)}
	s.Series = []loadtest.SecondBucket{
		{Second: 0, Warmup: true, Requests: 3, P50: 5, P90: 8, P95: 9, P99: 10, Max: 10, Mean: 6, Heat: []uint32{0, 3}},
		{Second: 1, Requests: 7, Errors: 2, ErrorsByClass: map[loadtest.ErrorClass]int64{loadtest.ClassHTTP5xx: 2}, InFlight: 2, P50: 12.3456, Max: 300, Heat: []uint32{1, 6}},
	}
	s.Health = []loadtest.HealthSample{{Second: 1, CPUCores: 0.5, Goroutines: 12, LagMs: 1.5}}
	s.MeasuredSeconds = 10
	s.WarmupRequests = 3
	s.Dropped = 1
	s.MaxLag = 4 * time.Millisecond
	s.FirstFailed = []loadtest.Result{{
		Step: "read/listPets", Operation: "GET /pets", Status: 500, Latency: 120 * time.Millisecond, ErrorClass: loadtest.ClassHTTP5xx,
		Detail: &loadtest.Detail{Method: "GET", URL: "http://x/pets", RequestHeaders: map[string][]string{"Authorization": {"[redacted]"}}, ResponseBody: "boom"},
	}}

	return &report.Input{
		Run: run, Summary: s,
		Environment: report.Environment{Version: "test", GoVersion: "go1.x", OS: "linux", Arch: "amd64", CPUs: 4},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)

	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}

	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file; run with -update if the change is intended.\n got: %.2000s", name, got)
	}
}

func TestJSON_Golden(t *testing.T) {
	t.Parallel()

	b, err := report.JSON(report.Build(fixture()))
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "report.json", b)
}

func TestJUnit_Golden(t *testing.T) {
	t.Parallel()

	b, err := report.JUnit(report.Build(fixture()))
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "report.xml", b)

	var parsed struct {
		Tests    int `xml:"tests,attr"`
		Failures int `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("junit is not well-formed: %v", err)
	}

	if parsed.Tests != 3 || parsed.Failures != 2 {
		t.Errorf("tests %d failures %d", parsed.Tests, parsed.Failures)
	}
}

func TestBuild_Numbers(t *testing.T) {
	t.Parallel()

	r := report.Build(fixture())

	if r.Totals.Requests != 10 || r.Totals.ErrorRate != 0.2 || r.Totals.ThroughputRPS != 1 || r.Totals.Dropped != 1 {
		t.Errorf("totals: %+v", r.Totals)
	}

	if r.Latency.Max < 299 || r.Latency.Max > 301 || r.Latency.P50 < 10 || r.Latency.P50 > 16 {
		t.Errorf("latency: %+v", r.Latency)
	}

	var sum int64
	for _, b := range r.Histogram {
		sum += b.Count
	}

	if sum != 10 {
		t.Errorf("histogram holds %d requests, want 10", sum)
	}

	if len(r.Operations) != 2 || r.Operations[0].Name != "getPet" || len(r.Status) != 2 || r.Status[0].Code != 200 {
		t.Errorf("slices must be sorted: %+v %+v", r.Operations, r.Status)
	}

	if len(r.Timings) != 2 || r.Timings[0].Phase != loadtest.PhaseTTFB {
		t.Errorf("timings: %+v", r.Timings)
	}

	if r.Thresholds[2].Observed != nil || r.Thresholds[1].Observed == nil {
		t.Errorf("threshold without data must have no observed value: %+v", r.Thresholds)
	}

	if r.Run.DurationSeconds != 12 {
		t.Errorf("duration %v", r.Run.DurationSeconds)
	}
}

func TestParse_RoundTrip(t *testing.T) {
	t.Parallel()

	b, err := report.JSON(report.Build(fixture()))
	if err != nil {
		t.Fatal(err)
	}

	got, err := report.Parse(b)
	if err != nil {
		t.Fatal(err)
	}

	again, _ := report.JSON(got)
	if string(again) != string(b) {
		t.Error("a parsed report must serialise to the same document")
	}

	if _, err := report.Parse([]byte("{")); err == nil {
		t.Error("expected a parse error")
	}
}

func TestBuild_WithoutSummary(t *testing.T) {
	t.Parallel()

	in := fixture()
	in.Summary = nil

	r := report.Build(in)
	if r.Totals.Requests != 0 || len(r.Histogram) == 0 {
		t.Errorf("empty report: %+v", r.Totals)
	}

	if _, err := report.JSON(r); err != nil {
		t.Error(err)
	}
}

func TestJUnit_AbortedAndFailedRuns(t *testing.T) {
	t.Parallel()

	in := fixture()
	in.Run.State = domain.LoadRunAborted
	in.Run.AbortReason = "errors.rate < 0.01 failed"

	b, _ := report.JUnit(report.Build(in))
	if !strings.Contains(string(b), "run aborted: errors.rate &lt; 0.01 failed") || !strings.Contains(string(b), `tests="4"`) {
		t.Errorf("aborted run must add a failing case:\n%s", b)
	}

	in.Run.State = domain.LoadRunFailed
	in.Run.Error = "dial tcp: refused"

	b, _ = report.JUnit(report.Build(in))
	if !strings.Contains(string(b), "run failed: dial tcp: refused") {
		t.Errorf("failed run:\n%s", b)
	}
}

func TestSchema_ListsEveryTopLevelField(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../docs/load-report.schema.json")
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}

	doc, _ := report.JSON(report.Build(fixture()))

	var got map[string]json.RawMessage
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}

	for k := range got {
		if _, ok := schema.Properties[k]; !ok {
			t.Errorf("report field %q is missing from the schema", k)
		}
	}

	for _, k := range schema.Required {
		if _, ok := got[k]; !ok {
			t.Errorf("required schema field %q is missing from the report", k)
		}
	}
}
