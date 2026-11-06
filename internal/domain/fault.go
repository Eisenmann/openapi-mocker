package domain

import (
	"errors"
	"fmt"
)

// MalformedMode says how a malformed response body is broken.
type MalformedMode string

const (
	// MalformedGarbage serves a body that is not valid JSON (half the body
	// followed by junk) with the rule's status and content type.
	MalformedGarbage MalformedMode = "garbage"
	// MalformedTruncated promises the full body, sends half of it and then
	// drops the connection, so the client sees an unexpected EOF.
	MalformedTruncated MalformedMode = "truncated"
	// MalformedEmpty serves the rule's status and content type with no body.
	MalformedEmpty MalformedMode = "empty"
)

// Fault limits.
const (
	// MaxLatencyMs caps DelayMs and JitterMs.
	MaxLatencyMs = 120_000
	// DefaultTimeoutMs is how long a timeout fault holds the request when
	// TimeoutMs is not set.
	DefaultTimeoutMs = 30_000
	// MaxTimeoutMs caps TimeoutMs; it stays below the server's write timeout.
	MaxTimeoutMs = 150_000

	percentMax     = 100
	minErrorStatus = 400
	maxErrorStatus = 599
)

// Errors returned by MockRule.ValidateFault.
var (
	ErrFaultLatency   = errors.New("delayMs and jitterMs must be between 0 and 120000")
	ErrFaultRate      = errors.New("fault rates must be between 0 and 100")
	ErrFaultRateTotal = errors.New("failRatePct, timeoutRatePct, dropRatePct and malformedRatePct add up to over 100")
	ErrFaultStatus    = errors.New("errorStatuses must be HTTP status codes between 400 and 599")
	ErrFaultTimeout   = errors.New("timeoutMs must be between 0 and 150000")
	ErrFaultMode      = errors.New("malformedMode must be one of: garbage, truncated, empty")
)

// Fault is the fault-injection part of a mock rule. Together with the rule's
// DelayMs (base latency) and FailRatePct (error rate) it lets a client be
// tested against slow, failing and broken servers.
//
// Each request rolls once: the rates are exclusive outcomes (drop, then
// timeout, then error, then malformed body) and whatever is left of the 100%
// is a normal response. Latency applies to every outcome.
type Fault struct {
	// JitterMs adds a random 0..JitterMs on top of the rule's DelayMs.
	JitterMs int `json:"jitterMs,omitempty"`
	// ErrorStatuses are the status codes used for injected errors (those of
	// FailRatePct); empty means a random 500-503.
	ErrorStatuses []int `json:"errorStatuses,omitempty"`
	// TimeoutRatePct is the share of requests that are never answered: the
	// server holds the connection for TimeoutMs and then drops it.
	TimeoutRatePct int `json:"timeoutRatePct,omitempty"`
	// TimeoutMs is the hold time of a timeout fault (default 30000).
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// DropRatePct is the share of requests whose connection is reset
	// without any response.
	DropRatePct int `json:"dropRatePct,omitempty"`
	// MalformedRatePct is the share of requests answered with a broken body.
	MalformedRatePct int `json:"malformedRatePct,omitempty"`
	// MalformedMode picks how the body is broken (default garbage).
	MalformedMode MalformedMode `json:"malformedMode,omitempty"`
}

// IsZero reports whether the fault does nothing.
func (f *Fault) IsZero() bool {
	return f == nil || (f.JitterMs == 0 && len(f.ErrorStatuses) == 0 && f.TimeoutRatePct == 0 &&
		f.TimeoutMs == 0 && f.DropRatePct == 0 && f.MalformedRatePct == 0 && f.MalformedMode == "")
}

// Mode returns the malformed mode with its default applied.
func (f *Fault) Mode() MalformedMode {
	if f == nil || f.MalformedMode == "" {
		return MalformedGarbage
	}

	return f.MalformedMode
}

// ValidateFault checks the rule's latency and fault settings.
func (r *MockRule) ValidateFault() error {
	f := r.Fault
	if f == nil {
		f = new(Fault)
	}

	err := r.validateLatency(f)
	if err != nil {
		return err
	}

	rates := []int{r.FailRatePct, f.TimeoutRatePct, f.DropRatePct, f.MalformedRatePct}
	total := 0

	for _, p := range rates {
		if p < 0 || p > percentMax {
			return ErrFaultRate
		}

		total += p
	}

	if total > percentMax {
		return fmt.Errorf("%w (got %d)", ErrFaultRateTotal, total)
	}

	return f.validateDetails()
}

func (r *MockRule) validateLatency(f *Fault) error {
	if r.DelayMs < 0 || r.DelayMs > MaxLatencyMs || f.JitterMs < 0 || f.JitterMs > MaxLatencyMs {
		return ErrFaultLatency
	}

	return nil
}

func (f *Fault) validateDetails() error {
	if f.TimeoutMs < 0 || f.TimeoutMs > MaxTimeoutMs {
		return ErrFaultTimeout
	}

	for _, s := range f.ErrorStatuses {
		if s < minErrorStatus || s > maxErrorStatus {
			return fmt.Errorf("%w (got %d)", ErrFaultStatus, s)
		}
	}

	switch f.MalformedMode {
	case "", MalformedGarbage, MalformedTruncated, MalformedEmpty:
		return nil
	default:
		return ErrFaultMode
	}
}
