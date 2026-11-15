package report_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
)

func TestHTML_SelfContainedAndComplete(t *testing.T) {
	t.Parallel()

	in := fixture()
	in.Run.Plan.Load = domain.LoadModel{Kind: domain.LoadOpen, Stages: []domain.Stage{{Duration: domain.Duration(10e9), Rate: 5}}}
	in.Run.Plan.Scenarios = []domain.Scenario{{Name: "s", Weight: 1, Steps: []domain.Step{{Method: "GET", Path: "/x"}}}}

	out, err := report.HTML(report.Build(in))
	if err != nil {
		t.Fatal(err)
	}

	page := string(out)

	for _, want := range []string{
		"FAILED", "http.latency.p95", "no data", "Latency percentiles over time", "Latency heatmap", "Where the time goes",
		"listPets", "getPet", "First failures", "GET http://x/pets", "[redacted]", "Load generator health", "prefers-color-scheme",
		"pets &lt;load&gt; &amp; test",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("report lacks %q", want)
		}
	}

	// Offline: nothing is fetched from anywhere.
	if m := regexp.MustCompile(`(?i)(src|href)\s*=|@import|url\(|<link`).FindString(page); m != "" {
		t.Errorf("report must not load external resources, found %q", m)
	}

	if strings.Contains(page, "pets <load>") {
		t.Error("the run name must be escaped")
	}

	if n := strings.Count(page, "<svg"); n < 6 {
		t.Errorf("charts: %d svgs", n)
	}
}

func TestHTML_EmptyAndAbortedRunsRender(t *testing.T) {
	t.Parallel()

	in := fixture()
	in.Summary = nil
	in.Run.State = domain.LoadRunAborted
	in.Run.AbortReason = "errors.rate failed"
	in.Run.Thresholds = nil

	out, err := report.HTML(report.Build(in))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(out), "ABORTED") || !strings.Contains(string(out), "errors.rate failed") ||
		!strings.Contains(string(out), "no thresholds") {
		t.Error("aborted run without data")
	}
}

func TestHTML_HourLongRunStaysSmall(t *testing.T) {
	t.Parallel()

	in := fixture()
	in.Run.Plan.Load = domain.LoadModel{Kind: domain.LoadOpen, Stages: []domain.Stage{{Duration: domain.Duration(3600e9), Rate: 500}}}

	const seconds = 3600

	series := make([]loadtest.SecondBucket, seconds)
	health := make([]loadtest.HealthSample, seconds)

	for i := range series {
		heat := make([]uint32, 40)
		for b := range heat {
			heat[b] = uint32((i*7 + b*13) % 50)
		}

		series[i] = loadtest.SecondBucket{
			Second: i + 1, Requests: 500, Errors: int64(i % 3), InFlight: 20,
			ErrorsByClass: map[loadtest.ErrorClass]int64{loadtest.ClassHTTP5xx: int64(i % 3), loadtest.ClassTimeout: int64(i % 2)},
			P50:           10 + float64(i%17), P90: 30, P95: 40 + float64(i%90), P99: 80, Max: 120 + float64(i%400), Mean: 15, Heat: heat,
		}
		health[i] = loadtest.HealthSample{Second: i + 1, CPUCores: 1.5, Goroutines: 100, GCPauseMaxMs: 0.5, SchedLatencyP99Ms: 0.2, LagMs: 1}
	}

	in.Summary.Series = series
	in.Summary.Health = health

	out, err := report.HTML(report.Build(in))
	if err != nil {
		t.Fatal(err)
	}

	if len(out) > 2<<20 {
		t.Errorf("a 1-hour report is %d bytes, the limit is 2 MiB", len(out))
	}

	t.Logf("1-hour report: %d KiB", len(out)/1024)

	if dump := os.Getenv("REPORT_DUMP"); dump != "" {
		_ = os.WriteFile(dump, out, 0o600)
	}
}
