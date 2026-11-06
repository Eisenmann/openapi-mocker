package usecase

import (
	"math/rand"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Injected faults, as reported in MockResponse.Fault and the request log.
const (
	FaultDrop      = "drop"      // connection reset, no response.
	FaultTimeout   = "timeout"   // request held, then the connection is dropped.
	FaultError     = "error"     // injected error status.
	FaultGarbage   = "garbage"   // body that is not valid JSON.
	FaultTruncated = "truncated" // half the body, then the connection is dropped.
	FaultEmpty     = "empty"     // no body.
)

// randIntn is the source of all chaos randomness; it needs to be unpredictable
// enough for testing, not cryptographically secure.
func randIntn(n int) int {
	return rand.Intn(n) //nolint:gosec // G404: fault injection is not security sensitive.
}

// garbageTail is appended to half of a body to make it invalid JSON.
const garbageTail = "<!-- malformed -->"

// faultSettings is everything that decides how faults hit a request: the
// base latency, the error rate and the rest of the fault. It is read from a
// REST mock rule or from an MCP tool's manifest.
type faultSettings struct {
	delayMs      int
	errorRatePct int
	fault        *domain.Fault
}

// settingsOf reads the fault settings of a REST mock rule.
func settingsOf(rule *domain.MockRule) faultSettings {
	return faultSettings{delayMs: rule.DelayMs, errorRatePct: rule.FailRatePct, fault: rule.Fault}
}

// rollFault decides which fault, if any, hits this request. The rates are
// exclusive slices of one 0-99 roll, in the order drop, timeout, error,
// malformed; a rule's rates add up to at most 100 (see ValidateFault).
func rollFault(s *faultSettings) string {
	f := s.fault
	if f == nil {
		f = new(domain.Fault)
	}

	roll := randIntn(PercentBase)

	for _, slice := range []struct {
		pct   int
		fault string
	}{
		{f.DropRatePct, FaultDrop},
		{f.TimeoutRatePct, FaultTimeout},
		{s.errorRatePct, FaultError},
		{f.MalformedRatePct, malformedFault(f.Mode())},
	} {
		if roll < slice.pct {
			return slice.fault
		}

		roll -= slice.pct
	}

	return ""
}

func malformedFault(mode domain.MalformedMode) string {
	switch mode {
	case domain.MalformedTruncated:
		return FaultTruncated
	case domain.MalformedEmpty:
		return FaultEmpty
	case domain.MalformedGarbage:
		return FaultGarbage
	default:
		return FaultGarbage
	}
}

// latencyMs is the base delay plus a random share of the jitter.
func (s *faultSettings) latencyMs() int {
	delay := s.delayMs
	if s.fault != nil && s.fault.JitterMs > 0 {
		delay += randIntn(s.fault.JitterMs + 1)
	}

	return delay
}

// errorStatus picks the status of an injected error: one of the
// ErrorStatuses, or a random 500-503.
func (s *faultSettings) errorStatus() int {
	if s.fault != nil && len(s.fault.ErrorStatuses) > 0 {
		return s.fault.ErrorStatuses[randIntn(len(s.fault.ErrorStatuses))]
	}

	return StatusInternalServerError + randIntn(ServerErrorRange)
}

// holdMs is how long a timeout fault holds the request.
func (s *faultSettings) holdMs() int {
	if s.fault != nil && s.fault.TimeoutMs > 0 {
		return s.fault.TimeoutMs
	}

	return domain.DefaultTimeoutMs
}

// faultResponse builds the response of a fault that replaces the rule's own
// response (drop, timeout and error); it is nil for any other outcome.
func faultResponse(rule *domain.MockRule, s *faultSettings, fault string, delayMs int) *MockResponse {
	resp := &MockResponse{DelayMs: delayMs, Fault: fault, MatchedRule: rule.ID, Matched: true}

	switch fault {
	case FaultDrop:
		resp.Source = "chaos-drop"
	case FaultTimeout:
		resp.Source = "chaos-timeout"
		resp.HoldMs = s.holdMs()
	case FaultError:
		resp.Source = "chaos-injection"
		resp.StatusCode = s.errorStatus()
		resp.ContentType = ContentTypeJSON
		resp.Body = errorBody("injected failure for chaos testing")
	default:
		return nil
	}

	return resp
}

// breakBody applies a malformed-body fault to a finished rule response.
func breakBody(resp *MockResponse, fault string) {
	resp.Fault = fault
	resp.Source = "chaos-malformed"
	resp.Body = brokenBody(resp.Body, fault)
}

// brokenBody returns body damaged the way the malformed fault asks for.
func brokenBody(body []byte, fault string) []byte {
	switch fault {
	case FaultEmpty:
		return []byte{}
	case FaultGarbage:
		return append(append([]byte{}, body[:len(body)/2]...), garbageTail...)
	default:
		// FaultTruncated: the HTTP adapter sends half of the body and drops
		// the connection, so the body is left whole here.
		return body
	}
}

// mcpFaultRun collects the faults rolled for the tools/call requests of one
// MCP HTTP request (several for a batch).
type mcpFaultRun struct {
	delayMs int
	fault   string
	holdMs  int
	status  int
}

// roll rolls the faults of one tools/call and returns the one that hit it.
// The request's latency is the largest of the calls'; the first call that is
// hit decides the fault of the whole HTTP request.
func (r *mcpFaultRun) roll(f *MCPFault) string {
	s := faultSettings{delayMs: f.DelayMs, errorRatePct: f.ErrorRatePct, fault: f.Settings}

	r.delayMs = max(r.delayMs, s.latencyMs())

	fault := rollFault(&s)
	if fault == "" || r.fault != "" {
		return fault
	}

	r.fault = fault

	switch fault {
	case FaultTimeout:
		r.holdMs = s.holdMs()
	case FaultError:
		r.status = s.errorStatus()
	}

	return fault
}

// apply turns the rolled fault into the response: status and body changes
// here, connection-level faults are left to the HTTP adapter.
func (r *mcpFaultRun) apply(resp *MCPResponse) {
	resp.Fault = r.fault

	switch r.fault {
	case FaultDrop:
		resp.Status = 0
	case FaultTimeout:
		resp.Status, resp.HoldMs = 0, r.holdMs
	case FaultError:
		resp.Status = r.status
	case FaultGarbage, FaultEmpty:
		if resp.Body != nil { // a notification has no body to break.
			resp.Body = brokenBody(resp.Body, r.fault)
		}
	case FaultTruncated:
		// The HTTP adapter sends half of the body and drops the connection.
	}
}
