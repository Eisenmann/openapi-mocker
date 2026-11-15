package usecase_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func genOps() []usecase.LoadOperation {
	return []usecase.LoadOperation{
		{ID: "listPets", Method: "GET", Path: "/pets", Safe: true},
		{Method: "GET", Path: "/pets/{id}", Safe: true},
		{Method: "HEAD", Path: "/pets", Safe: true},
		{ID: "createPet", Method: "POST", Path: "/pets"},
		{Method: "DELETE", Path: "/pets/{id}"},
	}
}

func TestGeneratePlan_Defaults(t *testing.T) {
	t.Parallel()

	plan, err := usecase.GeneratePlan(genOps(), &usecase.GenerateOptions{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Target.BaseURL != "mock://p1" || plan.Name != "p1 load test" {
		t.Errorf("target/name: %q %q", plan.Target.BaseURL, plan.Name)
	}

	if len(plan.Scenarios) != 1 || plan.Scenarios[0].Name != "read" || len(plan.Scenarios[0].Steps) != 3 {
		t.Fatalf("scenarios: %+v", plan.Scenarios)
	}

	for _, st := range plan.Scenarios[0].Steps {
		if st.Operation == "" || len(st.Checks) != 1 || st.Checks[0].Kind != domain.CheckContract {
			t.Errorf("step: %+v", st)
		}
	}

	lm := plan.Load
	if lm.Kind != domain.LoadOpen || len(lm.Stages) != 2 || lm.Stages[0].Rate != 10 || lm.Stages[1].Rate != 10 ||
		lm.Stages[0].Duration.Std() != time.Minute || lm.Stages[1].Duration.Std() != 5*time.Minute {
		t.Errorf("load: %+v", lm)
	}

	if len(plan.Thresholds) != 3 || plan.Thresholds[0].Metric != "http.latency.p95" || plan.Thresholds[0].Value != 500 {
		t.Errorf("thresholds: %+v", plan.Thresholds)
	}

	if err := plan.Validate(); err != nil {
		t.Errorf("generated plan must validate: %v", err)
	}

	if _, err := usecase.ResolveLoadTarget(plan, domain.RoleEditor, usecase.LoadCaps{}, "http://127.0.0.1:8080"); err != nil {
		t.Errorf("mock target must resolve: %v", err)
	}
}

func TestGeneratePlan_UnsafeIsOptIn(t *testing.T) {
	t.Parallel()

	plan, err := usecase.GeneratePlan(genOps(), &usecase.GenerateOptions{
		ProjectID: "p1", IncludeUnsafe: true, Rate: 50, Ramp: 10 * time.Second, Hold: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Scenarios) != 2 || plan.Scenarios[0].Weight != 9 || plan.Scenarios[1].Name != "write" ||
		plan.Scenarios[1].Weight != 1 || len(plan.Scenarios[1].Steps) != 2 {
		t.Errorf("scenarios: %+v", plan.Scenarios)
	}

	if plan.Load.Stages[0].Rate != 50 || plan.Load.Stages[0].Duration.Std() != 10*time.Second {
		t.Errorf("load: %+v", plan.Load)
	}
}

func TestGeneratePlan_Errors(t *testing.T) {
	t.Parallel()

	if _, err := usecase.GeneratePlan(nil, &usecase.GenerateOptions{}); !errors.Is(err, usecase.ErrLoadNoOperations) {
		t.Errorf("no operations: %v", err)
	}

	onlyWrites := []usecase.LoadOperation{{Method: "POST", Path: "/x"}}
	if _, err := usecase.GeneratePlan(onlyWrites, &usecase.GenerateOptions{}); !errors.Is(err, usecase.ErrLoadNoOperations) {
		t.Errorf("only unsafe operations: %v", err)
	}

	if p, err := usecase.GeneratePlan(onlyWrites, &usecase.GenerateOptions{IncludeUnsafe: true}); err != nil || len(p.Scenarios) != 1 {
		t.Errorf("unsafe only, opted in: %v %v", p, err)
	}
}

func TestGeneratePlan_ManyOperationsSplitIntoScenarios(t *testing.T) {
	t.Parallel()

	var ops []usecase.LoadOperation
	for i := range 450 {
		ops = append(ops, usecase.LoadOperation{Method: "GET", Path: "/r" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + "/" + string(rune('a'+i/676)), Safe: true})
	}

	plan, err := usecase.GeneratePlan(ops, &usecase.GenerateOptions{BaseURL: "https://api.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Scenarios) != 3 || plan.Scenarios[1].Name != "read-2" || plan.Target.BaseURL != "https://api.example.com" {
		t.Errorf("scenarios %d: %v", len(plan.Scenarios), plan.Scenarios[1].Name)
	}
}
