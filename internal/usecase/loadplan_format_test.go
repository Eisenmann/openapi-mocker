package usecase_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func richPlan() *domain.Plan {
	plan, err := usecase.GeneratePlan(genOps(), &usecase.GenerateOptions{ProjectID: "p1", IncludeUnsafe: true})
	if err != nil {
		panic(err)
	}

	st := &plan.Scenarios[0].Steps[0]
	st.Checks = append(st.Checks,
		domain.Check{Kind: domain.CheckStatus, Statuses: []int{200, 201}},
		domain.Check{Kind: domain.CheckLatency, MaxLatency: domain.Duration(800 * time.Millisecond)},
		domain.Check{Kind: domain.CheckJSONPath, Path: "id", Op: domain.CheckExists},
		domain.Check{Kind: domain.CheckJSONPath, Path: "name", Op: domain.CheckEquals, Value: "rex"},
		domain.Check{Kind: domain.CheckJSONPath, Path: "tags", Op: domain.CheckContains, Value: "a"},
	)
	st.Extracts = []domain.Extract{{Var: "petId", Path: "items[0].id"}}
	st.Think = &domain.Think{Kind: domain.ThinkConstant, Duration: domain.Duration(2 * time.Second)}
	plan.Thresholds = append(plan.Thresholds, domain.Threshold{
		Metric: "http.latency.p99", Scope: "listPets", Op: domain.OpLess, Value: 900,
		AbortOnFail: true, AbortDelay: domain.Duration(30 * time.Second),
	})

	return plan
}

func TestFormatPlanJSON_RoundTrip(t *testing.T) {
	t.Parallel()

	plan := richPlan()

	data, err := usecase.FormatPlanJSON(plan)
	if err != nil {
		t.Fatal(err)
	}

	back, err := usecase.ParsePlan(data)
	if err != nil {
		t.Fatalf("written plan must parse: %v\n%s", err, data)
	}

	if err := back.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if back.Name != plan.Name || back.Target.BaseURL != plan.Target.BaseURL {
		t.Errorf("header differs: %q %q", back.Name, back.Target.BaseURL)
	}

	if !reflect.DeepEqual(back.Load, plan.Load) {
		t.Errorf("load differs:\n got %+v\nwant %+v", back.Load, plan.Load)
	}

	if !reflect.DeepEqual(back.Thresholds, plan.Thresholds) {
		t.Errorf("thresholds differ:\n got %+v\nwant %+v", back.Thresholds, plan.Thresholds)
	}

	if len(back.Scenarios) != len(plan.Scenarios) {
		t.Fatalf("scenarios: %d vs %d", len(back.Scenarios), len(plan.Scenarios))
	}

	for _, want := range plan.Scenarios {
		found := false

		for _, got := range back.Scenarios {
			if got.Name == want.Name {
				found = true

				if !reflect.DeepEqual(got.Steps, want.Steps) || got.Weight != want.Weight {
					t.Errorf("scenario %s differs:\n got %+v\nwant %+v", want.Name, got, want)
				}
			}
		}

		if !found {
			t.Errorf("scenario %s lost", want.Name)
		}
	}
}
