//nolint:mnd,lll,cyclop,funlen,gocognit // chart geometry: positions and sizes are the point of this file
package report

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const noData = `<p class="muted">No data.</p>`

func maxSecond(r *Report) float64 {
	if len(r.Series) == 0 {
		return 1
	}

	return math.Max(float64(r.Series[len(r.Series)-1].Second), 1)
}

// warmupBand shades the warmup seconds of a time chart.
func warmupBand(b *strings.Builder, p plotArea, r *Report, maxSec float64) {
	warm := 0.0

	for i := range r.Series {
		if r.Series[i].Warmup {
			warm = float64(r.Series[i].Second)
		}
	}

	if warm <= 0 {
		return
	}

	w := p.width() * warm / maxSec
	fmt.Fprintf(b, `<rect class="warm" x="%g" y="%g" width="%s" height="%s"><title>warmup, not counted</title></rect>`,
		p.x0, p.y0, f1(w), f1(p.height()))
}

// percentilesChart is latency percentiles over time.
func percentilesChart(r *Report) template.HTML {
	if len(r.Series) == 0 {
		return noData
	}

	maxSec := maxSecond(r)
	top := 1.0

	for i := range r.Series {
		top = math.Max(top, r.Series[i].Max)
	}

	p := newPlot()
	xs := scale{0, maxSec, false}
	ys := scale{0, niceCeil(top), false}

	var b strings.Builder

	svgOpen(&b, "Latency percentiles over time")
	warmupBand(&b, p, r, maxSec)
	yAxis(&b, p, ys, "ms")
	xAxisSeconds(&b, p, maxSec)

	series := []struct {
		class string
		get   func(*Point) float64
	}{
		{"s1", func(q *Point) float64 { return q.P50 }},
		{"s2", func(q *Point) float64 { return q.P90 }},
		{"s3", func(q *Point) float64 { return q.P95 }},
		{"s4", func(q *Point) float64 { return q.P99 }},
		{"s5", func(q *Point) float64 { return q.Max }},
	}

	for _, s := range series {
		pts := make([]xy, 0, len(r.Series))

		for i := range r.Series {
			if r.Series[i].Requests > 0 {
				pts = append(pts, xy{float64(r.Series[i].Second), s.get(&r.Series[i])})
			}
		}

		polyline(&b, p, xs, ys, downsample(pts), s.class)
	}

	b.WriteString(`</svg>`)
	b.WriteString(legend([][2]string{{"s1", "p50"}, {"s2", "p90"}, {"s3", "p95"}, {"s4", "p99"}, {"s5", "max"}}))

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

// requestedRate is the requests per second the plan asks for at second t of
// an open model: the stage rates ramp linearly, and an iteration makes as many
// requests as the weighted average scenario has steps.
func requestedRate(plan *domain.Plan, t float64) (float64, bool) {
	if plan.Load.Kind != domain.LoadOpen {
		return 0, false
	}

	var start, prev float64

	for _, st := range plan.Load.Stages {
		dur := st.Duration.Std().Seconds()
		if t <= start+dur {
			rate := float64(st.Rate)

			return (prev + (rate-prev)*(t-start)/math.Max(dur, 1e-9)) * stepsPerIteration(plan), true
		}

		start += dur
		prev = float64(st.Rate)
	}

	return 0, false
}

func stepsPerIteration(plan *domain.Plan) float64 {
	var sum, weights float64

	for i := range plan.Scenarios {
		sum += float64(plan.Scenarios[i].Weight) * float64(len(plan.Scenarios[i].Steps))
		weights += float64(plan.Scenarios[i].Weight)
	}

	if weights == 0 {
		return 1
	}

	return sum / weights
}

// rateChart compares achieved requests per second with the requested rate.
func rateChart(r *Report) template.HTML {
	if len(r.Series) == 0 {
		return noData
	}

	maxSec := maxSecond(r)
	achieved := make([]xy, 0, len(r.Series))
	requested := make([]xy, 0, len(r.Series))
	top := 1.0

	for i := range r.Series {
		q := &r.Series[i]
		achieved = append(achieved, xy{float64(q.Second), float64(q.Requests)})
		top = math.Max(top, float64(q.Requests))

		if v, ok := requestedRate(&r.Plan, float64(q.Second)); ok {
			requested = append(requested, xy{float64(q.Second), v})
			top = math.Max(top, v)
		}
	}

	p := newPlot()
	xs := scale{0, maxSec, false}
	ys := scale{0, niceCeil(top), false}

	var b strings.Builder

	svgOpen(&b, "Achieved and requested request rate")
	warmupBand(&b, p, r, maxSec)
	yAxis(&b, p, ys, "requests/s")
	xAxisSeconds(&b, p, maxSec)
	polyline(&b, p, xs, ys, downsample(requested), "s5 dashed")
	polyline(&b, p, xs, ys, downsample(achieved), "s1")
	b.WriteString(`</svg>`)

	items := [][2]string{{"s1", "achieved"}}
	if len(requested) > 0 {
		items = append(items, [2]string{"s5", "requested"})
	}

	b.WriteString(legend(items))

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

// histogramChart is the latency distribution as bars plus its cumulative curve.
func histogramChart(r *Report) template.HTML {
	var total, peak int64

	for _, h := range r.Histogram {
		total += h.Count
		peak = max(peak, h.Count)
	}

	if total == 0 {
		return noData
	}

	p := newPlot()
	n := float64(len(r.Histogram))
	bw := p.width() / n
	ys := scale{0, float64(peak), false}

	var b strings.Builder

	svgOpen(&b, "Latency histogram and cumulative distribution")
	yAxis(&b, p, ys, "requests")

	cum := int64(0)
	curve := make([]string, 0, len(r.Histogram))

	for i, h := range r.Histogram {
		x := p.x0 + float64(i)*bw
		bh := ys.unit(float64(h.Count)) * p.height()

		if h.Count > 0 {
			fmt.Fprintf(&b, `<rect class="bar s1" x="%s" y="%s" width="%s" height="%s"><title>≤ %s ms: %d</title></rect>`,
				f1(x+0.5), f1(p.y1-bh), f1(math.Max(bw-1, 1)), f1(bh), num(h.UpperMs), h.Count)
		}

		cum += h.Count
		curve = append(curve, f1(x+bw)+","+f1(p.y1-float64(cum)/float64(total)*p.height()))

		if i%5 == 4 {
			fmt.Fprintf(&b, `<text class="tick" x="%s" y="%g" text-anchor="middle">%s</text>`, f1(x+bw), p.y1+16, num(h.UpperMs))
		}
	}

	b.WriteString(`<polyline class="line s4" points="` + strings.Join(curve, " ") + `"/>`)

	for _, share := range []int{0, 50, 100} {
		y := p.y1 - float64(share)/100*p.height()
		fmt.Fprintf(&b, `<text class="tick" x="%g" y="%s">%d%%</text>`, p.x1+3, f1(y+3.5), share)
	}

	fmt.Fprintf(&b, `<text class="unit" x="%g" y="%g" text-anchor="end">latency ms (log bins) · CDF share on the right</text>`, p.x1, p.y0-2)
	fmt.Fprintf(&b, `<line class="axis" x1="%g" x2="%g" y1="%g" y2="%g"/>`, p.x0, p.x1, p.y1, p.y1)
	b.WriteString(`</svg>`)
	b.WriteString(legend([][2]string{{"s1", "requests per latency bin"}, {"s4", "cumulative share"}}))

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

// columns groups seconds so charts have at most maxColumns columns.
func columns(n int) (size, count int) {
	size = int(math.Ceil(float64(n) / maxColumns))
	size = max(size, 1)

	return size, int(math.Ceil(float64(n) / float64(size)))
}

// heatmapChart is time against latency, shaded by how many requests fell in
// each cell.
func heatmapChart(r *Report) template.HTML {
	if len(r.Series) == 0 || len(r.HeatUpperMs) == 0 {
		return noData
	}

	size, count := columns(len(r.Series))
	bins := len(r.HeatUpperMs)
	grid := make([][]float64, count)
	peak := 0.0

	for c := range grid {
		grid[c] = make([]float64, bins)
	}

	for i := range r.Series {
		for bin, v := range r.Series[i].Heat {
			if bin < bins {
				grid[i/size][bin] += float64(v)
				peak = math.Max(peak, grid[i/size][bin])
			}
		}
	}

	if peak == 0 {
		return noData
	}

	p := newPlot()
	cw, ch := p.width()/float64(count), p.height()/float64(bins)

	var b strings.Builder

	svgOpen(&b, "Latency heatmap over time")

	for c := range grid {
		for bin, v := range grid[c] {
			if v == 0 {
				continue
			}

			op := 0.12 + 0.88*math.Log1p(v)/math.Log1p(peak)
			fmt.Fprintf(&b, `<rect class="cell" x="%s" y="%s" width="%s" height="%s" fill-opacity="%.2f"><title>%s s, ≤ %s ms: %s</title></rect>`,
				f1(p.x0+float64(c)*cw), f1(p.y1-float64(bin+1)*ch), f1(cw+0.4), f1(ch+0.4), op,
				clock(float64(c*size)), num(r.HeatUpperMs[bin]), num(v))
		}
	}

	for bin := 4; bin < bins; bin += 8 {
		y := p.y1 - float64(bin+1)*ch
		fmt.Fprintf(&b, `<text class="tick" x="%g" y="%s" text-anchor="end">%s</text>`, p.x0-6, f1(y+3.5), num(r.HeatUpperMs[bin]))
	}

	fmt.Fprintf(&b, `<text class="unit" x="%g" y="%g">latency ms (log scale)</text>`, p.x0, p.y0-2)
	xAxisSeconds(&b, p, maxSecond(r))
	b.WriteString(`</svg>`)

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

var errorClassOrder = []string{
	"timeout", "refused", "reset", "tls", "dns", "http_4xx", "http_5xx", "check_failed", "contract_violation",
}

// errorsChart stacks the failures of each time column by class.
func errorsChart(r *Report) template.HTML {
	size, count := columns(len(r.Series))
	cols := make([]map[string]int64, count)
	peak := int64(0)

	for i := range r.Series {
		c := i / size
		if cols[c] == nil {
			cols[c] = map[string]int64{}
		}

		for k, v := range r.Series[i].ErrorsByClass {
			cols[c][k] += v
		}
	}

	for _, col := range cols {
		var sum int64
		for _, v := range col {
			sum += v
		}

		peak = max(peak, sum)
	}

	if peak == 0 {
		return `<p class="muted">No errors.</p>`
	}

	p := newPlot()
	ys := scale{0, niceCeil(float64(peak)), false}
	bw := p.width() / float64(count)

	var b strings.Builder

	svgOpen(&b, "Errors over time by class")
	yAxis(&b, p, ys, "errors per column")
	xAxisSeconds(&b, p, maxSecond(r))

	used := map[string]bool{}

	for c, col := range cols {
		base := p.y1

		for i, class := range errorClassOrder {
			v := col[class]
			if v == 0 {
				continue
			}

			used[class] = true
			h := ys.unit(float64(v)) * p.height()
			base -= h
			fmt.Fprintf(&b, `<rect class="bar e%d" x="%s" y="%s" width="%s" height="%s"><title>%s: %d</title></rect>`,
				i, f1(p.x0+float64(c)*bw), f1(base), f1(math.Max(bw-0.5, 0.5)), f1(h), class, v)
		}
	}

	b.WriteString(`</svg>`)

	var items [][2]string

	for i, class := range errorClassOrder {
		if used[class] {
			items = append(items, [2]string{fmt.Sprintf("e%d", i), class})
		}
	}

	b.WriteString(legend(items))

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

// timingsChart shows where a request spends its time, by phase.
func timingsChart(r *Report) template.HTML {
	if len(r.Timings) == 0 {
		return noData
	}

	top := 0.001

	for _, t := range r.Timings {
		top = math.Max(top, t.Latency.P99)
	}

	xs := scale{0, niceCeil(top), false}
	rowH := 34.0
	h := float64(len(r.Timings))*rowH + 30
	p := plotArea{110, 8, chartW - 20, h - 22}

	var b strings.Builder

	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" role="img" aria-label="Timing breakdown by phase" preserveAspectRatio="xMidYMid meet">`, chartW, h)

	for i := 0; i <= yTicks; i++ {
		v := xs.hi * float64(i) / yTicks
		x := p.x0 + xs.unit(v)*p.width()
		fmt.Fprintf(&b, `<line class="grid" x1="%s" x2="%s" y1="%g" y2="%g"/>`, f1(x), f1(x), p.y0, p.y1)
		fmt.Fprintf(&b, `<text class="tick" x="%s" y="%g" text-anchor="middle">%s ms</text>`, f1(x), p.y1+15, num(v))
	}

	for i, t := range r.Timings {
		y := p.y0 + float64(i)*rowH
		fmt.Fprintf(&b, `<text class="tick" x="%g" y="%s" text-anchor="end">%s</text>`, p.x0-8, f1(y+rowH/2), t.Phase)
		fmt.Fprintf(&b, `<rect class="bar s4" x="%g" y="%s" width="%s" height="9"><title>p99 %s ms</title></rect>`,
			p.x0, f1(y+4), f1(xs.unit(t.Latency.P99)*p.width()), num(t.Latency.P99))
		fmt.Fprintf(&b, `<rect class="bar s1" x="%g" y="%s" width="%s" height="9"><title>p50 %s ms</title></rect>`,
			p.x0, f1(y+15), f1(xs.unit(t.Latency.P50)*p.width()), num(t.Latency.P50))
	}

	b.WriteString(`</svg>`)
	b.WriteString(legend([][2]string{{"s1", "p50"}, {"s4", "p99"}}))

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

// healthChart shows the load generator's own delays.
func healthChart(r *Report) template.HTML {
	if len(r.Health) == 0 {
		return noData
	}

	maxSec := maxSecond(r)
	top := 1.0
	lag := make([]xy, 0, len(r.Health))
	gc := make([]xy, 0, len(r.Health))
	sched := make([]xy, 0, len(r.Health))

	for _, h := range r.Health {
		x := float64(h.Second)
		lag = append(lag, xy{x, h.LagMs})
		gc = append(gc, xy{x, h.GCPauseMaxMs})
		sched = append(sched, xy{x, h.SchedLatencyP99Ms})
		top = math.Max(top, math.Max(h.LagMs, math.Max(h.GCPauseMaxMs, h.SchedLatencyP99Ms)))
	}

	p := newPlot()
	xs := scale{0, maxSec, false}
	ys := scale{0, niceCeil(top), false}

	var b strings.Builder

	svgOpen(&b, "Load generator delays")
	yAxis(&b, p, ys, "ms")
	xAxisSeconds(&b, p, maxSec)
	polyline(&b, p, xs, ys, downsample(lag), "s4")
	polyline(&b, p, xs, ys, downsample(sched), "s2")
	polyline(&b, p, xs, ys, downsample(gc), "s5")
	b.WriteString(`</svg>`)
	b.WriteString(legend([][2]string{{"s4", "start lag"}, {"s2", "scheduler wait p99"}, {"s5", "GC pause max"}}))

	return template.HTML(b.String()) //nolint:gosec // built from numbers and escaped text only
}

// sortedKeys returns the keys of m in order.
func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
