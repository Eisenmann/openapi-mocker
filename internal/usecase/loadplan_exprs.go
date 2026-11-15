package usecase

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	percentFactor = 100
	msPerSecond   = 1000
)

var (
	statusInRe  = regexp.MustCompile(`^status\s+in\s*\[([^\]]*)\]$`)
	statusEqRe  = regexp.MustCompile(`^status\s*(?:==|=)?\s*(\d{3})$`)
	latencyRe   = regexp.MustCompile(`^latency\s*(<=|<)\s*(\S+)$`)
	jsonPathRe  = regexp.MustCompile(`^(\$\S*)\s+(exists|==|contains)\s*(.*)$`)
	thresholdRe = regexp.MustCompile(
		`^([a-z0-9_.]+)\s*(?:\{\s*(?:operation|step|scope)\s*=\s*([^}]+?)\s*\})?\s*(<=|>=|==|!=|<|>)\s*(\S+)$`,
	)
	methodPathRe = regexp.MustCompile(`^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)\s+(/\S*)$`)
)

// jsonPathOf turns "$.items[0].id" into the dotted form used by
// domain.LookupJSONPath ("items[0].id"). "$" alone is the whole document.
func jsonPathOf(expr string) string {
	p := strings.TrimPrefix(expr, "$")

	return strings.TrimPrefix(p, ".")
}

// parseDuration reads "30s", "2m" or "300ms"; a bare number is refused
// because its unit would be a guess.
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, badValue("%q is not a duration with a unit such as 30s, 2m or 300ms", s)
	}

	return d, nil
}

// parseLatencyMs reads a latency threshold value: "300ms", "1.5s" or a bare
// number of milliseconds.
func parseLatencyMs(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err == nil {
		return f, nil
	}

	d, err := parseDuration(s)
	if err != nil {
		return 0, err
	}

	return float64(d) / float64(time.Millisecond), nil
}

// parseFraction reads "1%" as 0.01 and a plain number as it is.
func parseFraction(s string) (float64, error) {
	if num, ok := strings.CutSuffix(s, "%"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
		if err != nil {
			return 0, badValue("%q is not a percentage such as 1%%", s)
		}

		return f / percentFactor, nil
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, badValue("%q is not a number or a percentage", s)
	}

	return f, nil
}

// parseThresholdValue reads the right-hand side of a threshold according to
// its metric: latencies in time units, rates as percents, the rest as numbers.
func parseThresholdValue(metric, s string) (float64, error) {
	switch {
	case strings.HasPrefix(metric, "http.latency."):
		return parseLatencyMs(s)
	case metric == domain.MetricErrorsRate || metric == domain.MetricChecksRate:
		return parseFraction(s)
	default:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, badValue("%q is not a number", s)
		}

		return f, nil
	}
}

// parseThresholdExpr reads "http.latency.p95 < 500ms",
// "errors.rate < 1%" or "http.latency.p99{operation=listPets} < 900ms".
func parseThresholdExpr(expr string) (domain.Threshold, error) {
	var t domain.Threshold

	m := thresholdRe.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return t, badValue("%q is not a threshold like \"http.latency.p95 < 500ms\"", expr)
	}

	v, err := parseThresholdValue(m[1], m[4])
	if err != nil {
		return t, err
	}

	t.Metric = m[1]
	t.Scope = m[2]
	t.Op = domain.ThresholdOp(m[3])
	t.Value = v

	return t, nil
}

// parseCheckExpr reads a check such as "status in [200,201]", "status == 200",
// "$.id exists", "$.name == \"rex\"", "$.tags contains \"a\"",
// "latency < 800ms" or "contract".
func parseCheckExpr(expr string) (domain.Check, error) {
	var c domain.Check

	e := strings.TrimSpace(expr)

	switch {
	case e == "contract":
		c.Kind = domain.CheckContract

		return c, nil
	case strings.HasPrefix(e, "status"):
		return parseStatusCheck(e)
	case strings.HasPrefix(e, "latency"):
		return parseLatencyCheck(e)
	case strings.HasPrefix(e, "$"):
		return parseJSONPathCheck(e)
	default:
		return c, badValue("%q is not a check; use status, latency, $.path or contract", expr)
	}
}

func parseStatusCheck(e string) (domain.Check, error) {
	var c domain.Check

	c.Kind = domain.CheckStatus

	if m := statusInRe.FindStringSubmatch(e); m != nil {
		for _, part := range strings.Split(m[1], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return c, badValue("%q: %q is not a status code", e, strings.TrimSpace(part))
			}

			c.Statuses = append(c.Statuses, n)
		}

		return c, nil
	}

	if m := statusEqRe.FindStringSubmatch(e); m != nil {
		n, _ := strconv.Atoi(m[1])
		c.Statuses = []int{n}

		return c, nil
	}

	return c, badValue("%q is not a status check like \"status in [200,201]\" or \"status == 200\"", e)
}

func parseLatencyCheck(e string) (domain.Check, error) {
	var c domain.Check

	m := latencyRe.FindStringSubmatch(e)
	if m == nil {
		return c, badValue("%q is not a latency check like \"latency < 800ms\"", e)
	}

	d, err := parseDuration(m[2])
	if err != nil {
		return c, err
	}

	c.Kind = domain.CheckLatency
	c.MaxLatency = domain.Duration(d)

	return c, nil
}

func parseJSONPathCheck(e string) (domain.Check, error) {
	var c domain.Check

	m := jsonPathRe.FindStringSubmatch(e)
	if m == nil {
		return c, badValue("%q is not a path check like \"$.id exists\" or \"$.name == \\\"rex\\\"\"", e)
	}

	c.Kind = domain.CheckJSONPath
	c.Path = jsonPathOf(m[1])

	switch m[2] {
	case "exists":
		c.Op = domain.CheckExists
	case "==":
		c.Op = domain.CheckEquals
		c.Value = literalOf(m[3])
	default:
		c.Op = domain.CheckContains
		c.Value = literalOf(m[3])
	}

	if m[2] != "exists" && strings.TrimSpace(m[3]) == "" {
		return c, badValue("%q needs a value after %s", e, m[2])
	}

	return c, nil
}

// literalOf reads a JSON literal ("rex", 5, true); anything else is a string.
func literalOf(s string) any {
	s = strings.TrimSpace(s)

	var v any

	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}

	return s
}

// stepFromShorthand reads a step written as one string: "listPets" (an
// operationId) or "GET /pets/{id}".
func stepFromShorthand(s string) domain.Step {
	var st domain.Step

	if m := methodPathRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		st.Method = m[1]
		st.Path = m[2]

		return st
	}

	st.Operation = strings.TrimSpace(s)

	return st
}

// valueError is a problem with one value of a plan; the decoder adds the
// field path.
type valueError string

func (e valueError) Error() string { return string(e) }

func badValue(format string, args ...any) error {
	return valueError(fmt.Sprintf(format, args...))
}
