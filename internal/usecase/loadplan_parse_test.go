package usecase_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const goodPlanJSON = `{
  "name": "smoke",
  "target": {"baseUrl": "mock://p1", "http": {"protocol": "http2", "timeout": "10s", "maxConnsPerHost": 50},
             "auth": {"type": "bearer", "ref": "TOKEN"}},
  "data": {"users": {"file": "users.csv", "mode": "unique"}, "ids": {"file": "ids.json"}},
  "scenarios": {
    "buy": {"weight": 3, "steps": [
      "listPets",
      "POST /orders",
      {"name": "pay", "method": "post", "path": "/pay", "headers": {"X-A": "1"},
       "body": {"amount": 5, "note": "{{vars.x}}"},
       "checks": ["status in [200, 201]", "status == 204", "$.id exists", "$.name == \"rex\"", "$.n == 5",
                  "$.tags contains \"a\"", "latency < 800ms", "contract"],
       "extract": {"id": "$.items[0].id"},
       "think": {"kind": "uniform", "min": "100ms", "max": "2s"}}
    ]},
    "browse": {"steps": [{"operation": "GET /pets", "think": "1s"}]}
  },
  "load": {"model": "open", "stages": [{"duration": "1m", "rate": 10}, {"duration": "5m", "rate": "10"}],
           "warmup": "10s", "gracefulStop": "5s", "maxInFlight": 100, "contractSampleEvery": 10},
  "thresholds": [
    "http.latency.p95 < 500ms", "errors.rate < 1%", "checks.rate >= 99.5%", "throughput.rps > 20",
    "http.latency.p99{operation=listPets} <= 1.5s",
    {"expr": "errors.rate < 5%", "abortOnFail": true, "abortDelay": "30s"},
    {"metric": "http.latency.max", "op": "<", "value": "3s", "scope": "pay"},
    {"metric": "errors.count", "op": "==", "value": 0}
  ],
  "outputs": ["html", "junit"]
}`

func TestParsePlan_Full(t *testing.T) {
	t.Parallel()

	p, err := usecase.ParsePlan([]byte(goodPlanJSON))
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "smoke" || p.Target.BaseURL != "mock://p1" || p.Target.HTTP.Protocol != domain.ProtocolHTTP2 ||
		p.Target.HTTP.Timeout.Std() != 10*time.Second || p.Target.HTTP.MaxConnsPerHost != 50 ||
		p.Target.Auth == nil || p.Target.Auth.Ref != "TOKEN" {
		t.Errorf("target: %+v", p.Target)
	}

	if len(p.Data) != 2 || p.Data[0].Name != "ids" || p.Data[0].Format != domain.FeederJSON ||
		p.Data[1].Name != "users" || p.Data[1].Format != domain.FeederCSV || p.Data[1].Mode != domain.FeederUnique {
		t.Errorf("data: %+v", p.Data)
	}

	if len(p.Scenarios) != 2 || p.Scenarios[0].Name != "browse" || p.Scenarios[0].Weight != 1 ||
		p.Scenarios[1].Name != "buy" || p.Scenarios[1].Weight != 3 {
		t.Fatalf("scenarios sorted by name with default weight 1: %+v", p.Scenarios)
	}

	steps := p.Scenarios[1].Steps
	if steps[0].Operation != "listPets" || steps[1].Method != "POST" || steps[1].Path != "/orders" {
		t.Errorf("shorthand steps: %+v %+v", steps[0], steps[1])
	}

	pay := steps[2]
	if pay.Method != "POST" || pay.Headers["X-A"] != "1" || pay.Body != `{"amount":5,"note":"{{vars.x}}"}` {
		t.Errorf("pay step: %+v", pay)
	}

	if len(pay.Checks) != 8 || pay.Checks[0].Kind != domain.CheckStatus || len(pay.Checks[0].Statuses) != 2 ||
		pay.Checks[1].Statuses[0] != 204 || pay.Checks[2].Path != "id" || pay.Checks[2].Op != domain.CheckExists ||
		pay.Checks[3].Op != domain.CheckEquals || pay.Checks[3].Value != "rex" ||
		pay.Checks[4].Value != float64(5) || pay.Checks[5].Op != domain.CheckContains ||
		pay.Checks[6].MaxLatency.Std() != 800*time.Millisecond || pay.Checks[7].Kind != domain.CheckContract {
		t.Errorf("checks: %+v", pay.Checks)
	}

	if len(pay.Extracts) != 1 || pay.Extracts[0].Var != "id" || pay.Extracts[0].Path != "items[0].id" {
		t.Errorf("extracts: %+v", pay.Extracts)
	}

	if pay.Think == nil || pay.Think.Kind != domain.ThinkUniform || pay.Think.Max.Std() != 2*time.Second {
		t.Errorf("think: %+v", pay.Think)
	}

	if th := p.Scenarios[0].Steps[0].Think; th == nil || th.Kind != domain.ThinkConstant || th.Duration.Std() != time.Second {
		t.Errorf("constant think: %+v", th)
	}

	if p.Load.Kind != domain.LoadOpen || len(p.Load.Stages) != 2 || p.Load.Stages[1].Rate != 10 ||
		p.Warmup.Std() != 10*time.Second || p.GracefulStop.Std() != 5*time.Second || p.MaxInFlight != 100 ||
		p.ContractSampleEvery != 10 {
		t.Errorf("load: %+v warmup %v", p.Load, p.Warmup)
	}

	checkThresholds(t, p.Thresholds)

	if len(p.Outputs) != 2 || p.Outputs[1].Format != domain.OutputJUnit {
		t.Errorf("outputs: %+v", p.Outputs)
	}
}

func checkThresholds(t *testing.T, th []domain.Threshold) {
	t.Helper()

	if len(th) != 8 {
		t.Fatalf("%d thresholds", len(th))
	}

	if th[0].Metric != "http.latency.p95" || th[0].Op != domain.OpLess || th[0].Value != 500 {
		t.Errorf("latency threshold: %+v", th[0])
	}

	if th[1].Value != 0.01 || th[2].Op != domain.OpGreaterEqual || th[2].Value != 0.995 || th[3].Value != 20 {
		t.Errorf("rate thresholds: %+v %+v %+v", th[1], th[2], th[3])
	}

	if th[4].Scope != "listPets" || th[4].Value != 1500 || th[4].Op != domain.OpLessEqual {
		t.Errorf("scoped: %+v", th[4])
	}

	if !th[5].AbortOnFail || th[5].AbortDelay.Std() != 30*time.Second || th[5].Value != 0.05 {
		t.Errorf("abort: %+v", th[5])
	}

	if th[6].Metric != "http.latency.max" || th[6].Value != 3000 || th[6].Scope != "pay" || th[7].Value != 0 {
		t.Errorf("map thresholds: %+v %+v", th[6], th[7])
	}
}

func TestParsePlan_ErrorPaths(t *testing.T) {
	t.Parallel()

	base := func(mutate func(s string) string) []byte { return []byte(mutate(goodPlanJSON)) }

	cases := []struct {
		name string
		in   []byte
		path string
		frag string
	}{
		{
			"bad check", base(func(s string) string { return strings.Replace(s, `"latency < 800ms"`, `"speed is fast"`, 1) }),
			"scenarios.buy.steps[2].checks[6]", "not a check",
		},
		{
			"bad status check", base(func(s string) string { return strings.Replace(s, `"status == 204"`, `"status in [abc]"`, 1) }),
			"scenarios.buy.steps[2].checks[1]", "not a status code",
		},
		{
			"check not a string", base(func(s string) string { return strings.Replace(s, `"contract"`, `5`, 1) }),
			"scenarios.buy.steps[2].checks[7]", "check expression",
		},
		{
			"latency without unit", base(func(s string) string { return strings.Replace(s, `latency < 800ms`, `latency < 800`, 1) }),
			"scenarios.buy.steps[2].checks[6]", "not a duration",
		},
		{
			"extract path", base(func(s string) string { return strings.Replace(s, `"$.items[0].id"`, `"items"`, 1) }),
			"scenarios.buy.steps[2].extract.id", "JSON path",
		},
		{
			"unknown step field", base(func(s string) string { return strings.Replace(s, `"headers": {"X-A": "1"},`, `"header": {},`, 1) }),
			"scenarios.buy.steps[2].header", "unknown field",
		},
		{
			"bad duration", base(func(s string) string { return strings.Replace(s, `"timeout": "10s"`, `"timeout": 10`, 1) }),
			"target.http.timeout", "unit",
		},
		{
			"bad stage rate", base(func(s string) string { return strings.Replace(s, `"rate": "10"`, `"rate": "fast"`, 1) }),
			"load.stages[1].rate", "whole number",
		},
		{
			"stage not a map", base(func(s string) string { return strings.Replace(s, `{"duration": "1m", "rate": 10}`, `7`, 1) }),
			"load.stages[0]", "mapping",
		},
		{
			"bad threshold", base(func(s string) string { return strings.Replace(s, `"throughput.rps > 20"`, `"throughput is high"`, 1) }),
			"thresholds[3]", "not a threshold",
		},
		{
			"bad threshold value", base(func(s string) string { return strings.Replace(s, `errors.rate < 1%`, `errors.rate < lots`, 1) }),
			"thresholds[1]", "not a number or a percentage",
		},
		{
			"bad map threshold value", base(func(s string) string { return strings.Replace(s, `"value": "3s"`, `"value": true`, 1) }),
			"thresholds[6].value", "number or text",
		},
		{
			"unknown top field", base(func(s string) string {
				return strings.Replace(s, `"name": "smoke",`, `"name": "smoke", "extra": 1,`, 1)
			}),
			"extra", "unknown field",
		},
		{
			"outputs not strings", base(func(s string) string { return strings.Replace(s, `["html", "junit"]`, `["html", 3]`, 1) }),
			"outputs[1]", "string",
		},
		{
			"scenario not a map", base(func(s string) string {
				return strings.Replace(s, `"browse": {"steps": [{"operation": "GET /pets", "think": "1s"}]}`, `"browse": 5`, 1)
			}),
			"scenarios.browse", "mapping",
		},
		{
			"steps not a list", base(func(s string) string {
				return strings.Replace(s, `"steps": [{"operation": "GET /pets", "think": "1s"}]`, `"steps": "x"`, 1)
			}),
			"scenarios.browse.steps", "list",
		},
	}

	for _, tc := range cases {
		_, err := usecase.ParsePlan(tc.in)

		var pe *usecase.PlanError
		if !errors.As(err, &pe) {
			t.Errorf("%s: want a PlanError, got %v", tc.name, err)

			continue
		}

		if pe.Path != tc.path || !strings.Contains(pe.Msg, tc.frag) {
			t.Errorf("%s: path %q msg %q, want path %q containing %q", tc.name, pe.Path, pe.Msg, tc.path, tc.frag)
		}
	}
}

func TestParsePlan_ValidationAndSyntax(t *testing.T) {
	t.Parallel()

	if _, err := usecase.ParsePlan([]byte(`{"scenarios": {}}`)); !errors.Is(err, domain.ErrLoadNoBaseURL) {
		t.Errorf("a valid structure that fails domain validation: %v", err)
	}

	for _, in := range []string{"", "   ", "{not json"} {
		if _, err := usecase.ParsePlan([]byte(in)); !errors.Is(err, usecase.ErrPlanSyntax) {
			t.Errorf("%q: want ErrPlanSyntax, got %v", in, err)
		}
	}

	var pe *usecase.PlanError
	if _, err := usecase.ParsePlan([]byte(`[1,2]`)); !errors.As(err, &pe) {
		t.Errorf("a list at the top level must be a PlanError: %v", err)
	}
}

// The shipped examples must stay valid.
func TestParsePlan_Examples(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"load-plan.yaml", "load-plan-steps.yaml"} {
		data, err := os.ReadFile("../../examples/" + name)
		if err != nil {
			t.Fatal(err)
		}

		p, err := usecase.ParsePlan(data)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}

		if len(p.Scenarios) == 0 || len(p.Thresholds) == 0 {
			t.Errorf("%s parsed to an empty plan", name)
		}
	}
}
