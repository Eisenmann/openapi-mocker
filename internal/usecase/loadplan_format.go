package usecase

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	percentScale = 100
	roundTo      = 1e8
)

// The doc types describe a plan the way a plan file is written: the layout
// ParsePlan reads, with expressions for checks and thresholds. They carry both
// yaml and json tags so one tree is written as either.

type planDoc struct {
	Name        string                 `yaml:"name" json:"name"`
	Description string                 `yaml:"description,omitempty" json:"description,omitempty"`
	Target      targetDoc              `yaml:"target" json:"target"`
	Data        map[string]feederDoc   `yaml:"data,omitempty" json:"data,omitempty"`
	Scenarios   map[string]scenarioDoc `yaml:"scenarios" json:"scenarios"`
	Load        loadDoc                `yaml:"load" json:"load"`
	Thresholds  []any                  `yaml:"thresholds,omitempty" json:"thresholds,omitempty"`
	Outputs     []string               `yaml:"outputs,omitempty" json:"outputs,omitempty"`
}

type targetDoc struct {
	BaseURL    string   `yaml:"baseUrl" json:"baseUrl"`
	AllowHosts []string `yaml:"allowHosts,omitempty" json:"allowHosts,omitempty"`
	HTTP       *httpDoc `yaml:"http,omitempty" json:"http,omitempty"`
	Auth       *authDoc `yaml:"auth,omitempty" json:"auth,omitempty"`
}

type httpDoc struct {
	Protocol            string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Timeout             string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	DialTimeout         string `yaml:"dialTimeout,omitempty" json:"dialTimeout,omitempty"`
	TLSHandshakeTimeout string `yaml:"tlsHandshakeTimeout,omitempty" json:"tlsHandshakeTimeout,omitempty"`
	IdleConnTimeout     string `yaml:"idleConnTimeout,omitempty" json:"idleConnTimeout,omitempty"`
	DisableKeepAlives   bool   `yaml:"disableKeepAlives,omitempty" json:"disableKeepAlives,omitempty"`
	MaxConnsPerHost     int    `yaml:"maxConnsPerHost,omitempty" json:"maxConnsPerHost,omitempty"`
	MaxIdleConnsPerHost int    `yaml:"maxIdleConnsPerHost,omitempty" json:"maxIdleConnsPerHost,omitempty"`
	InsecureSkipVerify  bool   `yaml:"insecureSkipVerify,omitempty" json:"insecureSkipVerify,omitempty"`
}

type authDoc struct {
	Type   string `yaml:"type" json:"type"`
	Ref    string `yaml:"ref" json:"ref"`
	Header string `yaml:"header,omitempty" json:"header,omitempty"`
}

type feederDoc struct {
	File   string `yaml:"file" json:"file"`
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Mode   string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

type scenarioDoc struct {
	Weight int       `yaml:"weight" json:"weight"`
	Steps  []stepDoc `yaml:"steps" json:"steps"`
}

type stepDoc struct {
	Name      string            `yaml:"name,omitempty" json:"name,omitempty"`
	Operation string            `yaml:"operation,omitempty" json:"operation,omitempty"`
	Method    string            `yaml:"method,omitempty" json:"method,omitempty"`
	Path      string            `yaml:"path,omitempty" json:"path,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body      string            `yaml:"body,omitempty" json:"body,omitempty"`
	Checks    []string          `yaml:"checks,omitempty" json:"checks,omitempty"`
	Extract   map[string]string `yaml:"extract,omitempty" json:"extract,omitempty"`
	Think     any               `yaml:"think,omitempty" json:"think,omitempty"`
}

type thinkDoc struct {
	Kind     string `yaml:"kind" json:"kind"`
	Duration string `yaml:"duration,omitempty" json:"duration,omitempty"`
	Min      string `yaml:"min,omitempty" json:"min,omitempty"`
	Max      string `yaml:"max,omitempty" json:"max,omitempty"`
	Mean     string `yaml:"mean,omitempty" json:"mean,omitempty"`
	StdDev   string `yaml:"stdDev,omitempty" json:"stdDev,omitempty"`
}

type loadDoc struct {
	Model               string     `yaml:"model" json:"model"`
	Stages              []stageDoc `yaml:"stages,omitempty" json:"stages,omitempty"`
	Iterations          int        `yaml:"iterations,omitempty" json:"iterations,omitempty"`
	VUs                 int        `yaml:"vus,omitempty" json:"vus,omitempty"`
	PerVU               bool       `yaml:"perVu,omitempty" json:"perVu,omitempty"`
	Warmup              string     `yaml:"warmup,omitempty" json:"warmup,omitempty"`
	GracefulStop        string     `yaml:"gracefulStop,omitempty" json:"gracefulStop,omitempty"`
	MaxInFlight         int        `yaml:"maxInFlight,omitempty" json:"maxInFlight,omitempty"`
	ContractSampleEvery int        `yaml:"contractSampleEvery,omitempty" json:"contractSampleEvery,omitempty"`
}

type stageDoc struct {
	Duration string `yaml:"duration" json:"duration"`
	Rate     int    `yaml:"rate,omitempty" json:"rate,omitempty"`
	VUs      int    `yaml:"vus,omitempty" json:"vus,omitempty"`
}

type thresholdDoc struct {
	Expr        string `yaml:"expr" json:"expr"`
	AbortOnFail bool   `yaml:"abortOnFail,omitempty" json:"abortOnFail,omitempty"`
	AbortDelay  string `yaml:"abortDelay,omitempty" json:"abortDelay,omitempty"`
}

// FormatPlanYAML writes a plan as a YAML plan file that ParsePlan reads back.
func FormatPlanYAML(plan *domain.Plan) ([]byte, error) {
	out, err := yaml.Marshal(planDocOf(plan))
	if err != nil {
		return nil, fmt.Errorf("write plan: %w", err)
	}

	return out, nil
}

// FormatPlanJSON writes a plan as a JSON plan file that ParsePlan reads back.
func FormatPlanJSON(plan *domain.Plan) ([]byte, error) {
	out, err := json.MarshalIndent(planDocOf(plan), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("write plan: %w", err)
	}

	return append(out, '\n'), nil
}

func planDocOf(p *domain.Plan) *planDoc {
	d := new(planDoc)
	d.Name = p.Name
	d.Description = p.Description
	d.Target = targetDocOf(&p.Target)
	d.Scenarios = make(map[string]scenarioDoc, len(p.Scenarios))
	d.Load = loadDocOf(p)

	if len(p.Data) > 0 {
		d.Data = make(map[string]feederDoc, len(p.Data))
	}

	for i := range p.Data {
		f := &p.Data[i]
		d.Data[f.Name] = feederDoc{File: f.File, Format: string(f.Format), Mode: string(f.Mode)}
	}

	for i := range p.Scenarios {
		sc := &p.Scenarios[i]
		doc := scenarioDoc{Weight: sc.Weight}

		for j := range sc.Steps {
			doc.Steps = append(doc.Steps, stepDocOf(&sc.Steps[j]))
		}

		d.Scenarios[sc.Name] = doc
	}

	for i := range p.Thresholds {
		d.Thresholds = append(d.Thresholds, thresholdDocOf(&p.Thresholds[i]))
	}

	for _, o := range p.Outputs {
		d.Outputs = append(d.Outputs, string(o.Format))
	}

	return d
}

func targetDocOf(t *domain.Target) targetDoc {
	d := targetDoc{BaseURL: t.BaseURL, AllowHosts: t.AllowHosts}

	h := httpDoc{
		Protocol: string(t.HTTP.Protocol), Timeout: fmtDuration(t.HTTP.Timeout.Std()),
		DialTimeout:         fmtDuration(t.HTTP.DialTimeout.Std()),
		TLSHandshakeTimeout: fmtDuration(t.HTTP.TLSHandshakeTimeout.Std()),
		IdleConnTimeout:     fmtDuration(t.HTTP.IdleConnTimeout.Std()), DisableKeepAlives: t.HTTP.DisableKeepAlives,
		MaxConnsPerHost: t.HTTP.MaxConnsPerHost, MaxIdleConnsPerHost: t.HTTP.MaxIdleConnsPerHost,
		InsecureSkipVerify: t.HTTP.InsecureSkipVerify,
	}
	if h != (httpDoc{}) {
		d.HTTP = &h
	}

	if t.Auth != nil {
		d.Auth = &authDoc{Type: string(t.Auth.Type), Ref: t.Auth.Ref, Header: t.Auth.Header}
	}

	return d
}

func loadDocOf(p *domain.Plan) loadDoc {
	d := loadDoc{
		Model: string(p.Load.Kind), Iterations: p.Load.Iterations, VUs: p.Load.VUs, PerVU: p.Load.PerVU,
		Warmup: fmtDuration(p.Warmup.Std()), GracefulStop: fmtDuration(p.GracefulStop.Std()),
		MaxInFlight: p.MaxInFlight, ContractSampleEvery: p.ContractSampleEvery,
	}

	for _, s := range p.Load.Stages {
		d.Stages = append(d.Stages, stageDoc{Duration: fmtDuration(s.Duration.Std()), Rate: s.Rate, VUs: s.VUs})
	}

	return d
}

func stepDocOf(s *domain.Step) stepDoc {
	d := stepDoc{
		Name: s.Name, Operation: s.Operation, Method: s.Method, Path: s.Path, Headers: s.Headers, Body: s.Body,
	}

	for i := range s.Checks {
		d.Checks = append(d.Checks, checkExpr(&s.Checks[i]))
	}

	if len(s.Extracts) > 0 {
		d.Extract = make(map[string]string, len(s.Extracts))
		for _, e := range s.Extracts {
			d.Extract[e.Var] = "$." + e.Path
		}
	}

	if s.Think != nil {
		d.Think = thinkDocOf(s.Think)
	}

	return d
}

func thinkDocOf(t *domain.Think) any {
	if t.Kind == domain.ThinkConstant {
		return fmtDuration(t.Duration.Std())
	}

	return thinkDoc{
		Kind: string(t.Kind), Min: fmtDuration(t.Min.Std()), Max: fmtDuration(t.Max.Std()),
		Mean: fmtDuration(t.Mean.Std()), StdDev: fmtDuration(t.StdDev.Std()),
	}
}

// checkExpr writes a check as the expression parseCheckExpr reads.
func checkExpr(c *domain.Check) string {
	switch c.Kind {
	case domain.CheckContract:
		return "contract"
	case domain.CheckLatency:
		return "latency < " + fmtDuration(c.MaxLatency.Std())
	case domain.CheckStatus:
		codes := make([]string, 0, len(c.Statuses))
		for _, s := range c.Statuses {
			codes = append(codes, strconv.Itoa(s))
		}

		return "status in [" + strings.Join(codes, ", ") + "]"
	case domain.CheckJSONPath:
		return jsonPathExpr(c)
	default:
		return string(c.Kind)
	}
}

func jsonPathExpr(c *domain.Check) string {
	expr := "$." + c.Path + " " + string(c.Op)
	if c.Op == domain.CheckExists {
		return expr
	}

	lit, err := json.Marshal(c.Value)
	if err != nil {
		lit = []byte(fmt.Sprint(c.Value))
	}

	op := "=="
	if c.Op == domain.CheckContains {
		op = "contains"
	}

	return "$." + c.Path + " " + op + " " + string(lit)
}

// thresholdDocOf writes "metric{scope=x} < value" or, for an aborting
// threshold, a mapping with the expression and the abort settings.
func thresholdDocOf(t *domain.Threshold) any {
	expr := t.Metric
	if t.Scope != "" {
		expr += "{scope=" + t.Scope + "}"
	}

	expr += " " + string(t.Op) + " " + thresholdValueText(t)

	if !t.AbortOnFail {
		return expr
	}

	return thresholdDoc{Expr: expr, AbortOnFail: true, AbortDelay: fmtDuration(t.AbortDelay.Std())}
}

func thresholdValueText(t *domain.Threshold) string {
	switch {
	case strings.HasPrefix(t.Metric, "http.latency."):
		return strconv.FormatFloat(t.Value, 'f', -1, 64) + "ms"
	case t.Metric == domain.MetricErrorsRate || t.Metric == domain.MetricChecksRate:
		return strconv.FormatFloat(math.Round(t.Value*percentScale*roundTo)/roundTo, 'f', -1, 64) + "%"
	default:
		return strconv.FormatFloat(t.Value, 'f', -1, 64)
	}
}

// fmtDuration writes 1m, 5m, 30s or 1h30m instead of Go's 1m0s; zero is "".
func fmtDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}

	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}

	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}

	return s
}
