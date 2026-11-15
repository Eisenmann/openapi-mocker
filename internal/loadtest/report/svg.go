//nolint:mnd,lll,cyclop,funlen,gocognit // chart geometry: positions and sizes are the point of this file
package report

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
)

// Chart geometry, in SVG user units; the SVG scales to its container.
const (
	chartW      = 760.0
	chartH      = 240.0
	marginLeft  = 52.0
	marginRight = 30.0
	marginTop   = 12.0
	marginBot   = 28.0
	yTicks      = 4
	xTicks      = 6
	// maxPoints is the number of points a line is reduced to.
	maxPoints = 600
	// maxColumns is the number of columns of heatmap and bar charts.
	maxColumns = 120
)

// plotArea is the inner rectangle of a chart.
type plotArea struct {
	x0, y0, x1, y1 float64
}

func newPlot() plotArea {
	return plotArea{marginLeft, marginTop, chartW - marginRight, chartH - marginBot}
}

func (p plotArea) width() float64  { return p.x1 - p.x0 }
func (p plotArea) height() float64 { return p.y1 - p.y0 }

// scale maps a value of [lo, hi] linearly (or logarithmically) to [0, 1].
type scale struct {
	lo, hi float64
	log    bool
}

func (s scale) unit(v float64) float64 {
	if s.log {
		v = math.Log10(math.Max(v, s.lo))
		lo, hi := math.Log10(s.lo), math.Log10(s.hi)

		if hi == lo {
			return 0
		}

		return (v - lo) / (hi - lo)
	}

	if s.hi == s.lo {
		return 0
	}

	return (v - s.lo) / (s.hi - s.lo)
}

// niceCeil rounds v up to 1, 2, 2.5, 5 or 10 times a power of ten.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}

	exp := math.Pow(10, math.Floor(math.Log10(v)))

	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*exp {
			return m * exp
		}
	}

	return 10 * exp
}

// num formats an axis or value label compactly.
func num(v float64) string {
	switch {
	case v == 0:
		return "0"
	case math.Abs(v) >= 1e6:
		return strconv.FormatFloat(math.Round(v/1e4)/100, 'f', -1, 64) + "M"
	case math.Abs(v) >= 1e4:
		return strconv.FormatFloat(math.Round(v/10)/100, 'f', -1, 64) + "k"
	case math.Abs(v) >= 100:
		return strconv.FormatFloat(math.Round(v), 'f', -1, 64)
	default:
		return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	}
}

func f1(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

// svgOpen starts a chart; title and desc make it accessible.
func svgOpen(b *strings.Builder, title string) {
	fmt.Fprintf(b, `<svg viewBox="0 0 %g %g" role="img" aria-label="%s" preserveAspectRatio="xMidYMid meet">`,
		chartW, chartH, html.EscapeString(title))
	fmt.Fprintf(b, `<title>%s</title>`, html.EscapeString(title))
}

// yAxis draws horizontal grid lines with labels for a linear or log scale.
func yAxis(b *strings.Builder, p plotArea, sc scale, unit string) {
	ticks := yTickValues(sc)

	for _, t := range ticks {
		y := p.y1 - sc.unit(t)*p.height()
		fmt.Fprintf(b, `<line class="grid" x1="%g" x2="%g" y1="%s" y2="%s"/>`, p.x0, p.x1, f1(y), f1(y))
		fmt.Fprintf(b, `<text class="tick" x="%g" y="%s" text-anchor="end">%s</text>`, p.x0-6, f1(y+3.5), num(t))
	}

	if unit != "" {
		fmt.Fprintf(b, `<text class="unit" x="%g" y="%g">%s</text>`, p.x0, p.y0-2, html.EscapeString(unit))
	}
}

func yTickValues(sc scale) []float64 {
	var out []float64

	if sc.log {
		for e := math.Ceil(math.Log10(sc.lo)); math.Pow(10, e) <= sc.hi; e++ {
			out = append(out, math.Pow(10, e))
		}

		return out
	}

	for i := 0; i <= yTicks; i++ {
		out = append(out, sc.lo+(sc.hi-sc.lo)*float64(i)/yTicks)
	}

	return out
}

// xAxisSeconds labels the time axis in seconds.
func xAxisSeconds(b *strings.Builder, p plotArea, maxSec float64) {
	for i := 0; i <= xTicks; i++ {
		v := maxSec * float64(i) / xTicks
		x := p.x0 + p.width()*float64(i)/xTicks

		anchor := "middle"
		if i == xTicks {
			anchor = "end"
		}

		fmt.Fprintf(b, `<text class="tick" x="%s" y="%g" text-anchor="%s">%s</text>`, f1(x), p.y1+16, anchor, clock(v))
	}

	fmt.Fprintf(b, `<line class="axis" x1="%g" x2="%g" y1="%g" y2="%g"/>`, p.x0, p.x1, p.y1, p.y1)
}

// clock formats seconds as 45s, 12m, 1h05m.
func clock(sec float64) string {
	s := int(math.Round(sec))

	switch {
	case s < 60:
		return strconv.Itoa(s) + "s"
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	}
}

type xy struct{ x, y float64 }

// polyline draws a series; class selects its colour in the stylesheet.
func polyline(b *strings.Builder, p plotArea, xs, ys scale, pts []xy, class string) {
	if len(pts) == 0 {
		return
	}

	b.WriteString(`<polyline class="line ` + class + `" points="`)

	for i, pt := range pts {
		if i > 0 {
			b.WriteByte(' ')
		}

		x := p.x0 + xs.unit(pt.x)*p.width()
		y := p.y1 - ys.unit(pt.y)*p.height()
		b.WriteString(f1(x) + "," + f1(y))
	}

	b.WriteString(`"/>`)
}

// downsample reduces pts to at most limit points, keeping the maximum of each
// group so spikes stay visible.
func downsample(pts []xy) []xy {
	const limit = maxPoints

	if len(pts) <= limit {
		return pts
	}

	group := int(math.Ceil(float64(len(pts)) / float64(limit)))
	out := make([]xy, 0, limit)

	for i := 0; i < len(pts); i += group {
		end := min(i+group, len(pts))
		best := pts[i]

		for _, q := range pts[i:end] {
			if q.y > best.y {
				best = q
			}
		}

		out = append(out, best)
	}

	return out
}

// legend writes a legend row below a chart as HTML.
func legend(items [][2]string) string {
	var b strings.Builder

	b.WriteString(`<ul class="legend">`)

	for _, it := range items {
		fmt.Fprintf(&b, `<li><span class="swatch %s"></span>%s</li>`, it[0], html.EscapeString(it[1]))
	}

	b.WriteString(`</ul>`)

	return b.String()
}
