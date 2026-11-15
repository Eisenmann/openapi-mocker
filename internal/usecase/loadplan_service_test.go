package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type memPlans struct {
	mu sync.Mutex
	m  map[string][]domain.StoredPlan
}

func (s *memPlans) SavePlanVersion(_ context.Context, p *domain.StoredPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m == nil {
		s.m = map[string][]domain.StoredPlan{}
	}

	s.m[p.ProjectID+"/"+p.ID] = append(s.m[p.ProjectID+"/"+p.ID], *p)

	return nil
}

func (s *memPlans) GetPlan(_ context.Context, pr, id string, v int) (*domain.StoredPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	vs := s.m[pr+"/"+id]
	if len(vs) == 0 || v > len(vs) {
		return nil, domain.ErrLoadPlanNotFound
	}

	if v == 0 {
		v = len(vs)
	}

	p := vs[v-1]
	p.Latest = len(vs)

	return &p, nil
}

func (s *memPlans) ListPlans(context.Context, string) ([]domain.StoredPlan, error) { return nil, nil }

func (s *memPlans) ListPlanVersions(_ context.Context, pr, id string) ([]domain.StoredPlan, error) {
	return s.m[pr+"/"+id], nil
}

func (s *memPlans) DeletePlan(_ context.Context, pr, id string) error {
	delete(s.m, pr+"/"+id)

	return nil
}

func TestLoadPlanService_CreateUpdateVersions(t *testing.T) {
	t.Parallel()

	svc := usecase.NewLoadPlanService(&memPlans{})
	ctx := context.Background()

	plan, err := usecase.GeneratePlan(genOps(), &usecase.GenerateOptions{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}

	v1, err := svc.Create(ctx, "p1", plan, "alice")
	if err != nil || v1.Version != 1 || v1.ID == "" || v1.Name != plan.Name || v1.CreatedBy != "alice" {
		t.Fatalf("create: %+v %v", v1, err)
	}

	v2, err := svc.Update(ctx, "p1", v1.ID, plan, "bob")
	if err != nil || v2.Version != 2 {
		t.Fatalf("update: %+v %v", v2, err)
	}

	got, err := svc.Get(ctx, "p1", v1.ID, 0)
	if err != nil || got.Version != 2 || got.Latest != 2 {
		t.Fatalf("latest: %+v %v", got, err)
	}

	if _, err := svc.Update(ctx, "p1", "plan_missing", plan, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("update of an unknown plan: %v", err)
	}

	bad := *plan
	bad.Scenarios = nil

	if _, err := svc.Create(ctx, "p1", &bad, "x"); err == nil {
		t.Error("an invalid plan must be refused")
	}
}
