package domain

import (
	"errors"
	"time"
)

// LoadRunState is where a load run is in its life.
type LoadRunState string

// Load run states.
const (
	LoadRunPending  LoadRunState = "pending"
	LoadRunRunning  LoadRunState = "running"
	LoadRunPaused   LoadRunState = "paused"
	LoadRunFinished LoadRunState = "finished"
	LoadRunAborted  LoadRunState = "aborted"
	LoadRunFailed   LoadRunState = "failed"
)

// Active reports whether the run still occupies the project.
func (s LoadRunState) Active() bool {
	return s == LoadRunPending || s == LoadRunRunning || s == LoadRunPaused
}

// LoadVerdict is the outcome of a run against its thresholds.
type LoadVerdict string

// Load verdicts. A run that did not finish has no verdict.
const (
	VerdictPass LoadVerdict = "pass"
	VerdictFail LoadVerdict = "fail"
)

// Errors of load runs.
var (
	ErrLoadRunActive   = errors.New("the project already has the maximum number of active load runs")
	ErrLoadRunNotFound = errors.New("load run not found")
	ErrLoadRunNotLive  = errors.New("load run is not active")
)

// LoadThresholdResult is one threshold with what was measured.
type LoadThresholdResult struct {
	Threshold Threshold `json:"threshold"`
	// Observed is meaningless when HasData is false.
	Observed float64 `json:"observed"`
	HasData  bool    `json:"hasData"`
	Passed   bool    `json:"passed"`
}

// LoadTotals are the headline numbers of a run, kept on the run record so a
// history list needs no detail files.
type LoadTotals struct {
	Requests           int64   `json:"requests"`
	Errors             int64   `json:"errors"`
	ErrorRate          float64 `json:"errorRate"`
	ContractViolations int64   `json:"contractViolations"`
	Dropped            int64   `json:"dropped"`
	ThroughputRPS      float64 `json:"throughputRps"`
	LatencyP50Ms       float64 `json:"latencyP50Ms"`
	LatencyP95Ms       float64 `json:"latencyP95Ms"`
	LatencyP99Ms       float64 `json:"latencyP99Ms"`
	// Untrustworthy is set when the load generator could not keep up with the
	// plan, so the figures may be too optimistic.
	Untrustworthy       bool   `json:"untrustworthy,omitempty"`
	UntrustworthyReason string `json:"untrustworthyReason,omitempty"`
}

// LoadRun is the record of one run: what was run, against what, by whom and
// how it ended. Details (histograms, time series, samples) live beside it as
// artifacts.
type LoadRun struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	// PlanID and PlanVersion say which stored plan was run; both are empty
	// for an inline plan.
	PlanID      string `json:"planId,omitempty"`
	PlanVersion int    `json:"planVersion,omitempty"`
	// Plan is the snapshot that was run.
	Plan Plan `json:"plan"`
	// Target is the base URL requests went to, after mock:// was resolved.
	Target string `json:"target"`
	Actor  string `json:"actor,omitempty"`

	State       LoadRunState `json:"state"`
	Verdict     LoadVerdict  `json:"verdict,omitempty"`
	AbortReason string       `json:"abortReason,omitempty"`
	Error       string       `json:"error,omitempty"`

	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	Thresholds []LoadThresholdResult `json:"thresholds,omitempty"`
	Totals     *LoadTotals           `json:"totals,omitempty"`
}
