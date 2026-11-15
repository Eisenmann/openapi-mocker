package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
)

var csvHeader = []string{
	"second", "warmup", "requests", "errors", "in_flight", "vus", "dropped",
	"p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms",
}

// CSV renders the per-second time series of the run.
func CSV(r *Report) ([]byte, error) {
	var buf bytes.Buffer

	w := csv.NewWriter(&buf)

	err := w.Write(csvHeader)
	if err != nil {
		return nil, fmt.Errorf("write csv: %w", err)
	}

	for i := range r.Series {
		p := &r.Series[i]

		err = w.Write([]string{
			strconv.Itoa(p.Second), strconv.FormatBool(p.Warmup), i64(p.Requests), i64(p.Errors), i64(p.InFlight),
			i64(p.VUs), i64(p.Dropped), f64(p.P50), f64(p.P90), f64(p.P95), f64(p.P99), f64(p.Max), f64(p.Mean),
		})
		if err != nil {
			return nil, fmt.Errorf("write csv: %w", err)
		}
	}

	w.Flush()

	err = w.Error()
	if err != nil {
		return nil, fmt.Errorf("write csv: %w", err)
	}

	return buf.Bytes(), nil
}

func i64(v int64) string   { return strconv.FormatInt(v, 10) }
func f64(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
