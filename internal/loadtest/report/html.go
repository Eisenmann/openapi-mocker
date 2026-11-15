//nolint:mnd // display rounding
package report

import (
	"bytes"
	_ "embed" // The report template and stylesheet ship inside the binary.
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/report.html
var reportTemplate string

//go:embed templates/report.css
var reportCSS string

//go:embed templates/report.js
var reportJS string

// Card is one key number.
type Card struct {
	Label string
	Value string
	Note  string
	Warn  bool
}

type page struct {
	R           *Report
	CSS         template.CSS
	JS          template.JS
	VerdictText string
	VerdictCls  string
	Cards       []Card
	Charts      map[string]template.HTML
	Plan        string
	Slowest     []Sample
	Failed      []Sample
	Classes     []classCount
}

type classCount struct {
	Name  string
	Count int64
}

var funcs = template.FuncMap{
	"ms":    formatMs,
	"num":   num,
	"pct":   func(v float64) string { return strconv.FormatFloat(math.Round(v*10000)/100, 'f', -1, 64) + "%" },
	"int":   func(v int64) string { return strconv.FormatInt(v, 10) },
	"op":    func(v string) string { return v },
	"float": func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
	"time":  func(t *time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") },
	"hdrs":  headerLines,
	"deref": func(p *float64) float64 { return *p },
}

// formatMs renders a latency in milliseconds as µs, ms or s.
func formatMs(v float64) string {
	switch {
	case v == 0:
		return "0 ms"
	case v < 1:
		return strconv.FormatFloat(math.Round(v*1000), 'f', -1, 64) + " µs"
	case v < 1000:
		return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + " ms"
	default:
		return strconv.FormatFloat(math.Round(v/10)/100, 'f', -1, 64) + " s"
	}
}

func headerLines(h map[string][]string) string {
	var b strings.Builder

	for _, k := range sortedHeaderKeys(h) {
		b.WriteString(k + ": " + strings.Join(h[k], ", ") + "\n")
	}

	return b.String()
}

func sortedHeaderKeys(h map[string][]string) []string {
	m := make(map[string]int64, len(h))
	for k := range h {
		m[k] = 0
	}

	return sortedKeys(m)
}

// HTML renders the report as one self-contained page: no external scripts,
// fonts or images; charts are inline SVG.
func HTML(r *Report) ([]byte, error) {
	tmpl, err := template.New("report").Funcs(funcs).Parse(reportTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse report template: %w", err)
	}

	plan, err := json.MarshalIndent(r.Plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode plan: %w", err)
	}

	p := new(page)
	p.R = r
	p.CSS = template.CSS(reportCSS) //nolint:gosec // embedded, not user input
	p.JS = template.JS(reportJS)    //nolint:gosec // embedded, not user input
	p.VerdictText, p.VerdictCls = verdict(r)
	p.Cards = cards(r)
	p.Plan = string(plan)
	p.Slowest = r.Slowest
	p.Failed = r.FirstFailed
	p.Charts = map[string]template.HTML{
		"percentiles": percentilesChart(r), "rate": rateChart(r), "histogram": histogramChart(r),
		"heatmap": heatmapChart(r), "errors": errorsChart(r), "timings": timingsChart(r), "health": healthChart(r),
	}

	for _, k := range sortedKeys(r.Errors) {
		p.Classes = append(p.Classes, classCount{Name: k, Count: r.Errors[k]})
	}

	var buf bytes.Buffer

	err = tmpl.Execute(&buf, p)
	if err != nil {
		return nil, fmt.Errorf("render report: %w", err)
	}

	return buf.Bytes(), nil
}

// verdict is the headline and its style class.
func verdict(r *Report) (text, class string) {
	switch {
	case r.Run.State == "failed":
		return "FAILED TO RUN", classFail
	case r.Run.State == "aborted":
		return "ABORTED", classFail
	case r.Run.Verdict == "pass":
		return "PASSED", "pass"
	case r.Run.Verdict == "fail":
		return "FAILED", classFail
	default:
		return strings.ToUpper(r.Run.State), "neutral"
	}
}

func cards(r *Report) []Card {
	t := &r.Totals

	out := []Card{
		{"Requests", strconv.FormatInt(t.Requests, 10), fmt.Sprintf("%d s measured", t.MeasuredSeconds), false},
		{"Throughput", num(t.ThroughputRPS) + " req/s", "", false},
		{"Errors", strconv.FormatInt(t.Errors, 10), errorShare(t.ErrorRate), t.Errors > 0},
		{"Latency p50", formatMs(r.Latency.P50), "", false},
		{"Latency p95", formatMs(r.Latency.P95), "", false},
		{"Latency p99", formatMs(r.Latency.P99), "", false},
		{"Latency max", formatMs(r.Latency.Max), "", false},
		{"Contract violations", strconv.FormatInt(t.ContractViolations, 10), "", t.ContractViolations > 0},
	}

	if t.Dropped > 0 {
		out = append(out, Card{"Dropped", strconv.FormatInt(t.Dropped, 10), "arrivals skipped at maxInFlight", true})
	}

	if t.WarmupRequests > 0 {
		out = append(out, Card{"Warmup requests", strconv.FormatInt(t.WarmupRequests, 10), "not counted above", false})
	}

	return out
}

func errorShare(rate float64) string {
	return strconv.FormatFloat(rate*100, 'f', 2, 64) + "% of requests"
}

const classFail = "fail"
