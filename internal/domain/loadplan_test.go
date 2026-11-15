package domain_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func validPlan() domain.Plan {
	return domain.Plan{
		Name:   "smoke",
		Target: domain.Target{BaseURL: "mock://p1"},
		Scenarios: []domain.Scenario{{
			Name: "browse", Weight: 1,
			Steps: []domain.Step{{Method: "GET", Path: "/pets"}},
		}},
		Load: domain.LoadModel{Kind: domain.LoadOpen, Stages: []domain.Stage{
			{Duration: domain.Duration(time.Minute), Rate: 10},
		}},
	}
}

func TestPlan_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(p *domain.Plan)
		want   error
	}{
		{"valid", func(*domain.Plan) {}, nil},
		{"no base url", func(p *domain.Plan) { p.Target.BaseURL = "" }, domain.ErrLoadNoBaseURL},
		{"bad protocol", func(p *domain.Plan) { p.Target.HTTP.Protocol = "spdy" }, domain.ErrLoadProtocol},
		{"negative timeout", func(p *domain.Plan) { p.Target.HTTP.Timeout = -1 }, domain.ErrLoadHTTPOption},
		{"negative conns", func(p *domain.Plan) { p.Target.HTTP.MaxConnsPerHost = -1 }, domain.ErrLoadHTTPOption},
		{"auth without type", func(p *domain.Plan) { p.Target.Auth = &domain.AuthRef{Ref: "tok"} }, domain.ErrLoadAuth},
		{"auth header without name", func(p *domain.Plan) {
			p.Target.Auth = &domain.AuthRef{Type: domain.AuthHeader, Ref: "tok"}
		}, domain.ErrLoadAuth},
		{"auth without ref", func(p *domain.Plan) {
			p.Target.Auth = &domain.AuthRef{Type: domain.AuthBearer}
		}, domain.ErrLoadAuth},
		{"auth ok", func(p *domain.Plan) {
			p.Target.Auth = &domain.AuthRef{Type: domain.AuthBearer, Ref: "tok"}
		}, nil},
		{"feeder without file", func(p *domain.Plan) {
			p.Data = []domain.DataFeeder{{Name: "users", Format: domain.FeederCSV}}
		}, domain.ErrLoadFeeder},
		{"feeder bad mode", func(p *domain.Plan) {
			p.Data = []domain.DataFeeder{{Name: "u", File: "u.csv", Format: domain.FeederCSV, Mode: "loop"}}
		}, domain.ErrLoadFeeder},
		{"feeder duplicate", func(p *domain.Plan) {
			f := domain.DataFeeder{Name: "u", File: "u.csv", Format: domain.FeederCSV}
			p.Data = []domain.DataFeeder{f, f}
		}, domain.ErrLoadFeeder},
		{"no scenarios", func(p *domain.Plan) { p.Scenarios = nil }, domain.ErrLoadNoScenarios},
		{"scenario without name", func(p *domain.Plan) { p.Scenarios[0].Name = "" }, domain.ErrLoadScenarioName},
		{"duplicate scenario", func(p *domain.Plan) {
			p.Scenarios = append(p.Scenarios, p.Scenarios[0])
		}, domain.ErrLoadScenarioName},
		{"zero weight", func(p *domain.Plan) { p.Scenarios[0].Weight = 0 }, domain.ErrLoadWeight},
		{"no steps", func(p *domain.Plan) { p.Scenarios[0].Steps = nil }, domain.ErrLoadNoSteps},
		{"too many steps", func(p *domain.Plan) {
			p.Scenarios[0].Steps = make([]domain.Step, domain.MaxLoadSteps+1)
		}, domain.ErrLoadNoSteps},
		{"step without request", func(p *domain.Plan) { p.Scenarios[0].Steps[0] = domain.Step{Method: "GET"} }, domain.ErrLoadStepRequest},
		{"step from operation", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0] = domain.Step{Operation: "listPets"}
		}, nil},
		{"bad method", func(p *domain.Plan) { p.Scenarios[0].Steps[0].Method = "FETCH" }, domain.ErrLoadMethod},
		{"status check empty", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Checks = []domain.Check{{Kind: domain.CheckStatus}}
		}, domain.ErrLoadCheck},
		{"status check out of range", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Checks = []domain.Check{{Kind: domain.CheckStatus, Statuses: []int{700}}}
		}, domain.ErrLoadCheck},
		{"jsonpath check without op", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Checks = []domain.Check{{Kind: domain.CheckJSONPath, Path: "$.id"}}
		}, domain.ErrLoadCheck},
		{"latency check without limit", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Checks = []domain.Check{{Kind: domain.CheckLatency}}
		}, domain.ErrLoadCheck},
		{"unknown check", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Checks = []domain.Check{{Kind: "magic"}}
		}, domain.ErrLoadCheck},
		{"all checks ok", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Checks = []domain.Check{
				{Kind: domain.CheckStatus, Statuses: []int{200, 201}},
				{Kind: domain.CheckJSONPath, Path: "$.id", Op: domain.CheckExists},
				{Kind: domain.CheckLatency, MaxLatency: domain.Duration(800 * time.Millisecond)},
				{Kind: domain.CheckContract},
			}
		}, nil},
		{"extract without path", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Extracts = []domain.Extract{{Var: "id"}}
		}, domain.ErrLoadExtract},
		{"unknown think", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Think = &domain.Think{Kind: "zen"}
		}, domain.ErrLoadThink},
		{"uniform think reversed", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Think = &domain.Think{Kind: domain.ThinkUniform, Min: 5, Max: 1}
		}, domain.ErrLoadThink},
		{"normal think without mean", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Think = &domain.Think{Kind: domain.ThinkNormal}
		}, domain.ErrLoadThink},
		{"constant think negative", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Think = &domain.Think{Kind: domain.ThinkConstant, Duration: -1}
		}, domain.ErrLoadThink},
		{"lognormal think ok", func(p *domain.Plan) {
			p.Scenarios[0].Steps[0].Think = &domain.Think{Kind: domain.ThinkLognormal, Mean: 1000, StdDev: 200}
		}, nil},
		{"unknown model", func(p *domain.Plan) { p.Load.Kind = "burst" }, domain.ErrLoadModelKind},
		{"open without stages", func(p *domain.Plan) { p.Load.Stages = nil }, domain.ErrLoadNoStages},
		{"too many stages", func(p *domain.Plan) {
			p.Load.Stages = make([]domain.Stage, domain.MaxLoadStages+1)
		}, domain.ErrLoadNoStages},
		{"stage without duration", func(p *domain.Plan) { p.Load.Stages[0].Duration = 0 }, domain.ErrLoadStage},
		{"stage negative rate", func(p *domain.Plan) { p.Load.Stages[0].Rate = -1 }, domain.ErrLoadStage},
		{"open stage with vus", func(p *domain.Plan) { p.Load.Stages[0].VUs = 5 }, domain.ErrLoadModelMismatch},
		{"closed stage with rate", func(p *domain.Plan) { p.Load.Kind = domain.LoadClosed }, domain.ErrLoadModelMismatch},
		{"rate above limit", func(p *domain.Plan) { p.Load.Stages[0].Rate = domain.MaxLoadRate + 1 }, domain.ErrLoadRateLimit},
		{"vus above limit", func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadClosed, Stages: []domain.Stage{
				{Duration: domain.Duration(time.Minute), VUs: domain.MaxLoadVUs + 1},
			}}
		}, domain.ErrLoadVULimit},
		{"duration above limit", func(p *domain.Plan) {
			p.Load.Stages[0].Duration = domain.Duration(domain.MaxLoadDuration + time.Second)
		}, domain.ErrLoadDurationLimit},
		{"closed ok", func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadClosed, Stages: []domain.Stage{
				{Duration: domain.Duration(time.Minute), VUs: 20},
			}}
		}, nil},
		{"iterations without count", func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadIterations, VUs: 2}
		}, domain.ErrLoadIterations},
		{"iterations without vus", func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadIterations, Iterations: 10}
		}, domain.ErrLoadIterations},
		{"iterations vus above limit", func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadIterations, Iterations: 1, VUs: domain.MaxLoadVUs + 1}
		}, domain.ErrLoadVULimit},
		{"iterations ok", func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadIterations, Iterations: 100, VUs: 4, PerVU: true}
		}, nil},
		{"negative warmup", func(p *domain.Plan) { p.Warmup = -1 }, domain.ErrLoadTimeValue},
		{"negative maxInFlight", func(p *domain.Plan) { p.MaxInFlight = -1 }, domain.ErrLoadTimeValue},
		{"unknown metric", func(p *domain.Plan) {
			p.Thresholds = []domain.Threshold{{Metric: "cpu", Op: domain.OpLess, Value: 1}}
		}, domain.ErrLoadMetric},
		{"bad threshold op", func(p *domain.Plan) {
			p.Thresholds = []domain.Threshold{{Metric: domain.MetricErrorsRate, Op: "~", Value: 1}}
		}, domain.ErrLoadThresholdOp},
		{"negative threshold value", func(p *domain.Plan) {
			p.Thresholds = []domain.Threshold{{Metric: domain.MetricErrorsRate, Op: domain.OpLess, Value: -1}}
		}, domain.ErrLoadThresholdVal},
		{"thresholds ok", func(p *domain.Plan) {
			p.Thresholds = []domain.Threshold{
				{Metric: "http.latency.p95", Op: domain.OpLess, Value: 500, Scope: "listPets", AbortOnFail: true},
				{Metric: "http.latency.p99.9", Op: domain.OpLess, Value: 900},
				{Metric: domain.MetricContractViolations, Op: domain.OpEqual, Value: 0},
			}
		}, nil},
		{"unknown output", func(p *domain.Plan) { p.Outputs = []domain.Output{{Format: "pdf"}} }, domain.ErrLoadOutput},
		{"outputs ok", func(p *domain.Plan) {
			p.Outputs = []domain.Output{{Format: domain.OutputHTML}, {Format: domain.OutputJUnit}}
		}, nil},
	}

	for _, tc := range cases {
		p := validPlan()
		tc.mutate(&p)

		if err := p.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestIsKnownMetric(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{
		"http.latency.p50", "http.latency.p99", "http.latency.p99.9", "http.latency.p100", "http.latency.avg",
		"http.latency.max", "errors.rate", "errors.count", "checks.rate", "contract.violations",
		"throughput.rps", "dropped.count",
	} {
		if !domain.IsKnownMetric(ok) {
			t.Errorf("%q should be known", ok)
		}
	}

	for _, bad := range []string{"", "http.latency", "http.latency.p101", "http.latency.p", "latency", "errors"} {
		if domain.IsKnownMetric(bad) {
			t.Errorf("%q should be unknown", bad)
		}
	}
}

func TestDuration_JSON(t *testing.T) {
	t.Parallel()

	var d domain.Duration

	for in, want := range map[string]time.Duration{
		`"30s"`: 30 * time.Second, `"2m"`: 2 * time.Minute, `"300ms"`: 300 * time.Millisecond, `1500`: 1500 * time.Millisecond,
	} {
		if err := json.Unmarshal([]byte(in), &d); err != nil || d.Std() != want {
			t.Errorf("%s: got %v, %v want %v", in, d.Std(), err, want)
		}
	}

	for _, bad := range []string{`"soon"`, `true`, `{}`} {
		if err := json.Unmarshal([]byte(bad), &d); !errors.Is(err, domain.ErrLoadDuration) {
			t.Errorf("%s: got %v, want ErrLoadDuration", bad, err)
		}
	}

	out, err := json.Marshal(domain.Duration(90 * time.Second))
	if err != nil || string(out) != `"1m30s"` {
		t.Errorf("marshal: %s, %v", out, err)
	}
}

func TestLoadModel_TotalDuration(t *testing.T) {
	t.Parallel()

	m := domain.LoadModel{Stages: []domain.Stage{
		{Duration: domain.Duration(time.Minute)}, {Duration: domain.Duration(5 * time.Minute)},
	}}

	if got := m.TotalDuration(); got != 6*time.Minute {
		t.Errorf("got %v", got)
	}
}
