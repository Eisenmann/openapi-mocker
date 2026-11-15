package loadtest

import (
	"context"
	"time"
)

// ErrorClass says why a request counts as failed.
type ErrorClass string

// Error classes. The first group makes a request an error (errors.count,
// errors.rate); check_failed and contract_violation are counted per failed
// check or violation and do not turn an otherwise answered request into an
// error.
const (
	ClassTimeout           ErrorClass = "timeout"
	ClassRefused           ErrorClass = "refused"
	ClassReset             ErrorClass = "reset"
	ClassTLS               ErrorClass = "tls"
	ClassDNS               ErrorClass = "dns"
	ClassHTTP4xx           ErrorClass = "http_4xx"
	ClassHTTP5xx           ErrorClass = "http_5xx"
	ClassCheckFailed       ErrorClass = "check_failed"
	ClassContractViolation ErrorClass = "contract_violation"
)

// Timings is the breakdown of one request; a phase that did not happen (a
// reused connection has no DNS, connect or TLS) is zero.
type Timings struct {
	DNS      time.Duration
	Connect  time.Duration
	TLS      time.Duration
	TTFB     time.Duration
	Download time.Duration
}

// Detail is the (already redacted) request and response of a sample.
type Detail struct {
	Method          string
	URL             string
	RequestHeaders  map[string][]string
	RequestBody     string
	ResponseHeaders map[string][]string
	ResponseBody    string
	Error           string
}

// Result is what one request produced.
type Result struct {
	Scenario  string
	Step      string
	Operation string
	Status    int
	// Latency is measured from the intended start time in the open model and
	// from the real send time in the closed model.
	Latency time.Duration
	Timings Timings
	BytesIn int64
	// BytesOut is the request size.
	BytesOut int64
	// ErrorClass is empty for a request that was answered without an error.
	ErrorClass         ErrorClass
	ChecksPassed       int
	ChecksFailed       int
	ContractViolations int
	// Warmup marks results from the warmup period; they are not counted.
	Warmup bool
	// Detail is optional; the collector keeps it for the slowest and the
	// first failed requests.
	Detail *Detail
}

// IterationInfo tells an iteration what to run and when it was due.
type IterationInfo struct {
	// Scenario is the index of the scenario in the plan.
	Scenario int
	// VU numbers the virtual user (closed models) or the in-flight slot.
	VU int
	// Intended is when the iteration should have started. In the open model
	// latencies are measured from it, so a stalled target cannot hide delay.
	Intended time.Time
	// Warmup is true while the run is still in its warmup period.
	Warmup bool
}

// Iteration runs one scenario iteration, reporting each request's result to
// the collector itself. It must return when ctx is cancelled.
type Iteration func(ctx context.Context, info IterationInfo)
