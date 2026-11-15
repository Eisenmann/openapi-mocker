package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// LoadModelKind says how load is generated.
type LoadModelKind string

const (
	// LoadOpen is an arrival-rate model: iterations start at a requested rate
	// whatever the target does (no coordinated omission).
	LoadOpen LoadModelKind = "open"
	// LoadClosed is a VU model: a fixed number of virtual users loop their
	// scenario, so a slow target lowers the request rate.
	LoadClosed LoadModelKind = "closed"
	// LoadIterations runs a fixed number of iterations, shared or per VU.
	LoadIterations LoadModelKind = "iterations"
)

// ThinkKind is the distribution of a think-time pause.
type ThinkKind string

// Think-time distributions.
const (
	ThinkConstant  ThinkKind = "constant"
	ThinkUniform   ThinkKind = "uniform"
	ThinkNormal    ThinkKind = "normal"
	ThinkLognormal ThinkKind = "lognormal"
)

// CheckKind is the type of a response check.
type CheckKind string

// Response check types.
const (
	// CheckStatus passes when the status is one of Statuses.
	CheckStatus CheckKind = "status"
	// CheckJSONPath tests a value found by Path in the JSON body.
	CheckJSONPath CheckKind = "jsonpath"
	// CheckLatency passes when the response arrives within MaxLatency.
	CheckLatency CheckKind = "latency"
	// CheckContract passes when the response matches the contract.
	CheckContract CheckKind = "contract"
)

// CheckOp is the comparison of a JSON path check.
type CheckOp string

// JSON path check operations.
const (
	CheckExists   CheckOp = "exists"
	CheckEquals   CheckOp = "equals"
	CheckContains CheckOp = "contains"
)

// ThresholdOp compares a metric with a threshold value.
type ThresholdOp string

// Threshold comparison operators.
const (
	OpLess         ThresholdOp = "<"
	OpLessEqual    ThresholdOp = "<="
	OpGreater      ThresholdOp = ">"
	OpGreaterEqual ThresholdOp = ">="
	OpEqual        ThresholdOp = "=="
	OpNotEqual     ThresholdOp = "!="
)

// Protocol is the HTTP version used against the target.
type Protocol string

// HTTP protocol choices; empty means negotiate (HTTP/2 over TLS when offered).
const (
	ProtocolHTTP1 Protocol = "http1"
	ProtocolHTTP2 Protocol = "http2"
	ProtocolH2C   Protocol = "h2c"
)

// FeederMode says how data rows are handed to iterations.
type FeederMode string

// Feeder modes.
const (
	FeederSequential FeederMode = "sequential"
	FeederRandom     FeederMode = "random"
	// FeederUnique gives every row out once; the scenario stops when rows
	// run out and the run reports it.
	FeederUnique FeederMode = "unique"
)

// FeederFormat is the file format of a data feeder.
type FeederFormat string

// Feeder file formats.
const (
	FeederCSV  FeederFormat = "csv"
	FeederJSON FeederFormat = "json"
)

// AuthKind is how a target is authenticated.
type AuthKind string

// Auth kinds.
const (
	AuthBearer AuthKind = "bearer"
	AuthBasic  AuthKind = "basic"
	AuthHeader AuthKind = "header"
)

// OutputFormat is a report format.
type OutputFormat string

// Report formats.
const (
	OutputHTML  OutputFormat = "html"
	OutputJSON  OutputFormat = "json"
	OutputJUnit OutputFormat = "junit"
	OutputCSV   OutputFormat = "csv"
)

// Plan limits. Server-wide caps from the environment are applied on top of
// these when a run starts.
const (
	MaxLoadRate     = 100_000
	MaxLoadVUs      = 100_000
	MaxLoadDuration = 24 * time.Hour
	MaxLoadStages   = 100
	MaxLoadSteps    = 200

	minHTTPStatus = 100
	maxHTTPStatus = 599
)

// Metric names a threshold can be set on.
const (
	MetricErrorsRate         = "errors.rate"
	MetricErrorsCount        = "errors.count"
	MetricChecksRate         = "checks.rate"
	MetricContractViolations = "contract.violations"
	MetricThroughputRPS      = "throughput.rps"
	MetricDroppedCount       = "dropped.count"
	MetricLatencyAvg         = "http.latency.avg"
	MetricLatencyMin         = "http.latency.min"
	MetricLatencyMax         = "http.latency.max"
)

var latencyPercentileMetric = regexp.MustCompile(`^http\.latency\.p(100|[1-9]?\d(\.\d+)?)$`)

// Errors returned by Plan.Validate.
var (
	ErrLoadNoBaseURL     = errors.New("target.baseUrl is required")
	ErrLoadProtocol      = errors.New("target.http.protocol must be one of: http1, http2, h2c")
	ErrLoadHTTPOption    = errors.New("http options must not be negative")
	ErrLoadAuth          = errors.New("auth needs a type (bearer, basic or header) and a ref")
	ErrLoadFeeder        = errors.New("a data feeder needs a unique name, a file, format csv or json and a valid mode")
	ErrLoadNoScenarios   = errors.New("a plan needs at least one scenario")
	ErrLoadScenarioName  = errors.New("scenario names must be set and unique")
	ErrLoadWeight        = errors.New("scenario weight must be greater than 0")
	ErrLoadNoSteps       = errors.New("a scenario needs between 1 and 200 steps")
	ErrLoadStepRequest   = errors.New("a step needs either an operation or a method and a path")
	ErrLoadMethod        = errors.New("step method is not a known HTTP method")
	ErrLoadCheck         = errors.New("check is incomplete or has an unknown type")
	ErrLoadExtract       = errors.New("an extract needs a var name and a JSON path")
	ErrLoadThink         = errors.New("think time is invalid for its distribution")
	ErrLoadModelKind     = errors.New("load.kind must be one of: open, closed, iterations")
	ErrLoadNoStages      = errors.New("load.stages must have between 1 and 100 stages for this model")
	ErrLoadStage         = errors.New("a stage needs a positive duration and non-negative rate or vus")
	ErrLoadRateLimit     = errors.New("stage rate is above the plan limit of 100000 per second")
	ErrLoadVULimit       = errors.New("vus are above the plan limit of 100000")
	ErrLoadDurationLimit = errors.New("total duration is above the plan limit of 24h")
	ErrLoadIterations    = errors.New("iterations model needs iterations > 0 and vus > 0")
	ErrLoadModelMismatch = errors.New("stage uses rate or vus that the load model does not use")
	ErrLoadTimeValue     = errors.New("warmup, gracefulStop, maxInFlight and contractSampleEvery must not be negative")
	ErrLoadMetric        = errors.New("threshold metric is not known")
	ErrLoadThresholdOp   = errors.New("threshold op must be one of: <, <=, >, >=, ==, !=")
	ErrLoadThresholdVal  = errors.New("threshold value must not be negative")
	ErrLoadOutput        = errors.New("output format must be one of: html, json, junit, csv")
	ErrLoadDuration      = errors.New("duration must be a string like 30s or a number of milliseconds")
)

// Duration is a time.Duration that is written as "30s" and read from either
// a string ("2m", "300ms") or a number of milliseconds.
type Duration time.Duration

// Std returns the standard library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalJSON writes the duration as a string such as "1m30s".
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON reads "30s" or a number of milliseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var ms float64

	err := json.Unmarshal(b, &ms)
	if err == nil {
		*d = Duration(time.Duration(ms * float64(time.Millisecond)))

		return nil
	}

	var s string

	err = json.Unmarshal(b, &s)
	if err != nil {
		return fmt.Errorf("%w (got %s)", ErrLoadDuration, b)
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%w (got %q)", ErrLoadDuration, s)
	}

	*d = Duration(parsed)

	return nil
}

// Plan is a complete load test: what to hit, how to generate load, what to
// check and which results to keep.
type Plan struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Target      Target `json:"target"`
	// Data lists the feeders scenarios can read rows from.
	Data      []DataFeeder `json:"data,omitempty"`
	Scenarios []Scenario   `json:"scenarios"`
	Load      LoadModel    `json:"load"`
	// Warmup is excluded from statistics.
	Warmup Duration `json:"warmup,omitempty"`
	// GracefulStop is how long in-flight iterations may run after the load
	// ends or the run is aborted.
	GracefulStop Duration `json:"gracefulStop,omitempty"`
	// MaxInFlight caps concurrent requests of an open model; iterations that
	// would exceed it are counted as dropped. 0 means the server default.
	MaxInFlight int `json:"maxInFlight,omitempty"`
	// ContractSampleEvery validates 1 of N responses against the contract;
	// 0 or 1 validates all of them.
	ContractSampleEvery int         `json:"contractSampleEvery,omitempty"`
	Thresholds          []Threshold `json:"thresholds,omitempty"`
	Outputs             []Output    `json:"outputs,omitempty"`
}

// Target is the system under load.
type Target struct {
	// BaseURL is an http(s) URL, or mock://<projectId> for this server's own
	// mock of a project.
	BaseURL string `json:"baseUrl"`
	// AllowHosts lists the hosts load may be sent to: exact names or
	// *.suffix patterns.
	AllowHosts []string    `json:"allowHosts,omitempty"`
	HTTP       HTTPOptions `json:"http"`
	Auth       *AuthRef    `json:"auth,omitempty"`
}

// HTTPOptions tunes the client used for a run; zero values mean defaults.
type HTTPOptions struct {
	Protocol            Protocol `json:"protocol,omitempty"`
	Timeout             Duration `json:"timeout,omitempty"`
	DialTimeout         Duration `json:"dialTimeout,omitempty"`
	TLSHandshakeTimeout Duration `json:"tlsHandshakeTimeout,omitempty"`
	IdleConnTimeout     Duration `json:"idleConnTimeout,omitempty"`
	DisableKeepAlives   bool     `json:"disableKeepAlives,omitempty"`
	MaxConnsPerHost     int      `json:"maxConnsPerHost,omitempty"`
	MaxIdleConnsPerHost int      `json:"maxIdleConnsPerHost,omitempty"`
	InsecureSkipVerify  bool     `json:"insecureSkipVerify,omitempty"`
}

// AuthRef points at credentials by name; the secret itself never lives in a
// plan.
type AuthRef struct {
	Type AuthKind `json:"type"`
	// Ref names the stored secret or environment variable.
	Ref string `json:"ref"`
	// Header is the header name for type header.
	Header string `json:"header,omitempty"`
}

// DataFeeder is a CSV or JSON file whose rows feed {{data.<name>.<col>}}.
type DataFeeder struct {
	Name   string       `json:"name"`
	File   string       `json:"file"`
	Format FeederFormat `json:"format"`
	Mode   FeederMode   `json:"mode,omitempty"`
}

// Scenario is a weighted sequence of steps one virtual user runs per
// iteration.
type Scenario struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
	Steps  []Step `json:"steps"`
}

// Step is one request. It comes either from a contract operation (Operation
// is an operationId or "GET /path") or is spelled out with Method and Path.
type Step struct {
	Name      string            `json:"name,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Checks    []Check           `json:"checks,omitempty"`
	Extracts  []Extract         `json:"extracts,omitempty"`
	Think     *Think            `json:"think,omitempty"`
}

// Check is an assertion on a response. A failed check is counted, it does
// not stop the run.
type Check struct {
	Kind       CheckKind `json:"kind"`
	Statuses   []int     `json:"statuses,omitempty"`
	Path       string    `json:"path,omitempty"`
	Op         CheckOp   `json:"op,omitempty"`
	Value      any       `json:"value,omitempty"`
	MaxLatency Duration  `json:"maxLatency,omitempty"`
}

// Extract saves a JSON path of the response into a per-iteration variable
// ({{vars.<name>}}).
type Extract struct {
	Var  string `json:"var"`
	Path string `json:"path"`
}

// Think is a pause between steps.
type Think struct {
	Kind ThinkKind `json:"kind"`
	// Duration is the pause of a constant think time.
	Duration Duration `json:"duration,omitempty"`
	// Min and Max bound a uniform pause.
	Min Duration `json:"min,omitempty"`
	Max Duration `json:"max,omitempty"`
	// Mean and StdDev describe a normal or lognormal pause.
	Mean   Duration `json:"mean,omitempty"`
	StdDev Duration `json:"stdDev,omitempty"`
}

// LoadModel describes how load grows and holds.
type LoadModel struct {
	Kind LoadModelKind `json:"kind"`
	// Stages ramp linearly from the previous stage's value to this one's.
	Stages []Stage `json:"stages,omitempty"`
	// Iterations and VUs drive the iterations model.
	Iterations int `json:"iterations,omitempty"`
	VUs        int `json:"vus,omitempty"`
	// PerVU makes Iterations a count per virtual user instead of a shared one.
	PerVU bool `json:"perVu,omitempty"`
}

// Stage is one ramp or hold. Rate is used by the open model and VUs by the
// closed model.
type Stage struct {
	Duration Duration `json:"duration"`
	Rate     int      `json:"rate,omitempty"`
	VUs      int      `json:"vus,omitempty"`
}

// Threshold is an SLO: Metric Op Value must hold for the run to pass.
// Latencies are in milliseconds, rates are fractions (1% is 0.01).
type Threshold struct {
	Metric string      `json:"metric"`
	Op     ThresholdOp `json:"op"`
	Value  float64     `json:"value"`
	// Scope limits the metric to one operation or step; empty means all.
	Scope string `json:"scope,omitempty"`
	// AbortOnFail stops the run as soon as the threshold fails, after
	// AbortDelay so early noise does not abort it.
	AbortOnFail bool     `json:"abortOnFail,omitempty"`
	AbortDelay  Duration `json:"abortDelay,omitempty"`
}

// Output asks for a report in one format.
type Output struct {
	Format OutputFormat `json:"format"`
}

// IsKnownMetric reports whether a threshold can be set on the metric.
func IsKnownMetric(name string) bool {
	switch name {
	case MetricErrorsRate, MetricErrorsCount, MetricChecksRate, MetricContractViolations,
		MetricThroughputRPS, MetricDroppedCount, MetricLatencyAvg, MetricLatencyMin, MetricLatencyMax:
		return true
	default:
		return latencyPercentileMetric.MatchString(name)
	}
}

// TotalDuration is the sum of the stage durations.
func (m LoadModel) TotalDuration() time.Duration {
	var total time.Duration

	for _, s := range m.Stages {
		total += s.Duration.Std()
	}

	return total
}

// Validate checks the plan and returns the first problem found. Errors wrap
// one of the ErrLoad* values and name the offending field.
func (p *Plan) Validate() error {
	steps := []func() error{
		p.Target.validate,
		p.validateFeeders,
		p.validateScenarios,
		p.Load.validate,
		p.validateTuning,
		p.validateThresholds,
		p.validateOutputs,
	}

	for _, step := range steps {
		err := step()
		if err != nil {
			return err
		}
	}

	return nil
}

func (t *Target) validate() error {
	if t.BaseURL == "" {
		return ErrLoadNoBaseURL
	}

	err := t.HTTP.validate()
	if err != nil {
		return err
	}

	if t.Auth == nil {
		return nil
	}

	return t.Auth.validate()
}

func (h *HTTPOptions) validate() error {
	switch h.Protocol {
	case "", ProtocolHTTP1, ProtocolHTTP2, ProtocolH2C:
	default:
		return ErrLoadProtocol
	}

	if h.Timeout < 0 || h.DialTimeout < 0 || h.TLSHandshakeTimeout < 0 || h.IdleConnTimeout < 0 ||
		h.MaxConnsPerHost < 0 || h.MaxIdleConnsPerHost < 0 {
		return ErrLoadHTTPOption
	}

	return nil
}

func (a *AuthRef) validate() error {
	switch a.Type {
	case AuthBearer, AuthBasic:
	case AuthHeader:
		if a.Header == "" {
			return fmt.Errorf("%w (target.auth.header)", ErrLoadAuth)
		}
	default:
		return fmt.Errorf("%w (target.auth.type)", ErrLoadAuth)
	}

	if a.Ref == "" {
		return fmt.Errorf("%w (target.auth.ref)", ErrLoadAuth)
	}

	return nil
}

func (p *Plan) validateFeeders() error {
	seen := make(map[string]bool, len(p.Data))

	for i, f := range p.Data {
		okFormat := f.Format == FeederCSV || f.Format == FeederJSON
		okMode := slices.Contains([]FeederMode{"", FeederSequential, FeederRandom, FeederUnique}, f.Mode)

		if f.Name == "" || f.File == "" || seen[f.Name] || !okFormat || !okMode {
			return fmt.Errorf("%w (data[%d])", ErrLoadFeeder, i)
		}

		seen[f.Name] = true
	}

	return nil
}

func (p *Plan) validateScenarios() error {
	if len(p.Scenarios) == 0 {
		return ErrLoadNoScenarios
	}

	seen := make(map[string]bool, len(p.Scenarios))

	for i, sc := range p.Scenarios {
		path := fmt.Sprintf("scenarios[%d]", i)

		if sc.Name == "" || seen[sc.Name] {
			return fmt.Errorf("%w (%s)", ErrLoadScenarioName, path)
		}

		seen[sc.Name] = true

		if sc.Weight <= 0 {
			return fmt.Errorf("%w (%s)", ErrLoadWeight, path)
		}

		err := sc.validateSteps(path)
		if err != nil {
			return err
		}
	}

	return nil
}

func (sc Scenario) validateSteps(path string) error {
	if len(sc.Steps) == 0 || len(sc.Steps) > MaxLoadSteps {
		return fmt.Errorf("%w (%s)", ErrLoadNoSteps, path)
	}

	for i := range sc.Steps {
		err := sc.Steps[i].validate()
		if err != nil {
			return fmt.Errorf("%s.steps[%d]: %w", path, i, err)
		}
	}

	return nil
}

var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

func (s *Step) validate() error {
	if s.Operation == "" && (s.Method == "" || s.Path == "") {
		return ErrLoadStepRequest
	}

	if s.Method != "" && !slices.Contains(httpMethods, s.Method) {
		return fmt.Errorf("%w (%q)", ErrLoadMethod, s.Method)
	}

	err := s.validateAssertions()
	if err != nil {
		return err
	}

	if s.Think == nil {
		return nil
	}

	return s.Think.validate()
}

func (s *Step) validateAssertions() error {
	for i := range s.Checks {
		err := s.Checks[i].validate()
		if err != nil {
			return fmt.Errorf("checks[%d]: %w", i, err)
		}
	}

	for i, e := range s.Extracts {
		if e.Var == "" || e.Path == "" {
			return fmt.Errorf("extracts[%d]: %w", i, ErrLoadExtract)
		}
	}

	return nil
}

func (c *Check) validate() error {
	switch c.Kind {
	case CheckStatus:
		return c.validateStatuses()
	case CheckJSONPath:
		return c.validateJSONPath()
	case CheckLatency:
		return c.validateLatency()
	case CheckContract:
		return nil
	default:
		return ErrLoadCheck
	}
}

func (c *Check) validateStatuses() error {
	if len(c.Statuses) == 0 {
		return ErrLoadCheck
	}

	for _, s := range c.Statuses {
		if s < minHTTPStatus || s > maxHTTPStatus {
			return fmt.Errorf("%w (status %d)", ErrLoadCheck, s)
		}
	}

	return nil
}

func (c *Check) validateJSONPath() error {
	okOp := c.Op == CheckExists || c.Op == CheckEquals || c.Op == CheckContains
	if c.Path == "" || !okOp {
		return ErrLoadCheck
	}

	return nil
}

func (c *Check) validateLatency() error {
	if c.MaxLatency <= 0 {
		return ErrLoadCheck
	}

	return nil
}

func (t *Think) validate() error {
	switch t.Kind {
	case ThinkConstant:
		if t.Duration < 0 {
			return ErrLoadThink
		}
	case ThinkUniform:
		if t.Min < 0 || t.Max < t.Min {
			return ErrLoadThink
		}
	case ThinkNormal, ThinkLognormal:
		if t.Mean <= 0 || t.StdDev < 0 {
			return ErrLoadThink
		}
	default:
		return ErrLoadThink
	}

	return nil
}

func (m LoadModel) validate() error {
	switch m.Kind {
	case LoadOpen, LoadClosed:
		return m.validateStages()
	case LoadIterations:
		return m.validateIterations()
	default:
		return ErrLoadModelKind
	}
}

func (m LoadModel) validateStages() error {
	if len(m.Stages) == 0 || len(m.Stages) > MaxLoadStages {
		return ErrLoadNoStages
	}

	for i, s := range m.Stages {
		err := s.validate(m.Kind)
		if err != nil {
			return fmt.Errorf("%w (load.stages[%d])", err, i)
		}
	}

	if m.TotalDuration() > MaxLoadDuration {
		return ErrLoadDurationLimit
	}

	return nil
}

func (s Stage) validate(kind LoadModelKind) error {
	if s.Duration <= 0 || s.Rate < 0 || s.VUs < 0 {
		return ErrLoadStage
	}

	if (kind == LoadOpen && s.VUs != 0) || (kind == LoadClosed && s.Rate != 0) {
		return ErrLoadModelMismatch
	}

	return s.validateLimits()
}

func (s Stage) validateLimits() error {
	switch {
	case s.Rate > MaxLoadRate:
		return ErrLoadRateLimit
	case s.VUs > MaxLoadVUs:
		return ErrLoadVULimit
	default:
		return nil
	}
}

func (m LoadModel) validateIterations() error {
	if m.Iterations <= 0 || m.VUs <= 0 {
		return ErrLoadIterations
	}

	if m.VUs > MaxLoadVUs {
		return ErrLoadVULimit
	}

	return nil
}

func (p *Plan) validateTuning() error {
	if p.Warmup < 0 || p.GracefulStop < 0 || p.MaxInFlight < 0 || p.ContractSampleEvery < 0 {
		return ErrLoadTimeValue
	}

	return nil
}

func (p *Plan) validateThresholds() error {
	for i, t := range p.Thresholds {
		path := fmt.Sprintf("thresholds[%d]", i)

		if !IsKnownMetric(t.Metric) {
			return fmt.Errorf("%w (%s: %q)", ErrLoadMetric, path, t.Metric)
		}

		switch t.Op {
		case OpLess, OpLessEqual, OpGreater, OpGreaterEqual, OpEqual, OpNotEqual:
		default:
			return fmt.Errorf("%w (%s)", ErrLoadThresholdOp, path)
		}

		if t.Value < 0 || t.AbortDelay < 0 {
			return fmt.Errorf("%w (%s)", ErrLoadThresholdVal, path)
		}
	}

	return nil
}

func (p *Plan) validateOutputs() error {
	for i, o := range p.Outputs {
		switch o.Format {
		case OutputHTML, OutputJSON, OutputJUnit, OutputCSV:
		default:
			return fmt.Errorf("%w (outputs[%d])", ErrLoadOutput, i)
		}
	}

	return nil
}
