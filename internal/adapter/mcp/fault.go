package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// faultKey is the reserved key that declares fault injection, either on a
// tool or inside one of its mockResponses entries (the entry wins). Like
// "match" it is removed from the result that is served.
const faultKey = "fault"

// faultSpec is a "fault" object of a manifest: the fields of a REST mock
// rule's fault plus its delayMs and error rate, e.g.
//
//	{"delayMs": 200, "jitterMs": 50, "errorRatePct": 10, "errorStatuses": [503],
//	 "timeoutRatePct": 5, "dropRatePct": 5, "malformedRatePct": 5, "malformedMode": "truncated"}
//
// failRatePct is accepted as another name of errorRatePct (as in REST rules).
type faultSpec struct {
	domain.Fault

	DelayMs      int `json:"delayMs"`
	ErrorRatePct int `json:"errorRatePct"`
	FailRatePct  int `json:"failRatePct"`
}

// parseFault decodes and validates one "fault" object. Unknown fields are an
// error, so a misspelled rate does not silently do nothing.
func parseFault(raw json.RawMessage) (*faultSpec, error) {
	var f faultSpec

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	err := dec.Decode(&f)
	if err != nil {
		return nil, fmt.Errorf(`"fault" must be an object of fault settings: %w`, err)
	}

	var rule domain.MockRule

	rule.DelayMs, rule.FailRatePct, rule.Fault = f.DelayMs, f.errorRate(), &f.Fault

	err = rule.ValidateFault()
	if err != nil {
		return nil, fmt.Errorf("invalid fault: %w", err)
	}

	return &f, nil
}

// errorRate is the share of calls answered with an injected error.
func (f *faultSpec) errorRate() int {
	if f.ErrorRatePct != 0 {
		return f.ErrorRatePct
	}

	return f.FailRatePct
}

// settings converts the spec to what the usecase layer rolls on.
func (f *faultSpec) settings() *usecase.MCPFault {
	fault := f.Fault

	return &usecase.MCPFault{DelayMs: f.DelayMs, ErrorRatePct: f.errorRate(), Settings: &fault}
}

// entryFault returns the fault declared inside a mockResponses entry, or nil.
func entryFault(raw json.RawMessage) (*faultSpec, error) {
	var obj map[string]json.RawMessage

	if json.Unmarshal(raw, &obj) != nil {
		return nil, nil //nolint:nilnil,nilerr // not an object: it declares no fault.
	}

	f, ok := obj[faultKey]
	if !ok {
		return nil, nil //nolint:nilnil // no fault declared.
	}

	return parseFault(f)
}

// toolFault returns the fault declared on the tool itself, or nil.
func (t *manifestTool) toolFault() (*faultSpec, error) {
	if len(t.Fault) == 0 {
		return nil, nil //nolint:nilnil // no fault declared.
	}

	return parseFault(t.Fault)
}

// faultFor returns the fault that applies to a call answered by the
// mockResponses entry key: the entry's own fault, else the tool's.
func (t *manifestTool) faultFor(key string) *faultSpec {
	if raw, ok := t.MockResponses[key]; ok {
		f, err := entryFault(raw)
		if err == nil && f != nil {
			return f
		}
	}

	f, err := t.toolFault()
	if err != nil {
		return nil
	}

	return f
}

// injectsError rolls the fault injection of a call answered by the entry key
// and reports whether it turns the call into an error.
func (st *callState) injectsError(tool *manifestTool, key string) bool {
	if st.fault == nil {
		return false
	}

	f := tool.faultFor(key)

	return f != nil && st.fault(f.settings()) == usecase.FaultError
}

// checkFaults reports a malformed "fault" on the tool or in any response.
func (t *manifestTool) checkFaults() error {
	_, err := t.toolFault()
	if err != nil {
		return err
	}

	for key, raw := range t.MockResponses {
		_, err = entryFault(raw)
		if err != nil {
			return fmt.Errorf("mockResponses.%s: %w", key, err)
		}
	}

	return nil
}
