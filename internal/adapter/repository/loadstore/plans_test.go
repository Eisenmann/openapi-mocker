package loadstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/loadstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func newPlanStore(t *testing.T) *loadstore.Store {
	t.Helper()

	s, err := loadstore.New(t.TempDir(), &loadstore.Options{})
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestPlans_VersionsAndLatest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newPlanStore(t)

	for v := 1; v <= 3; v++ {
		p := &domain.StoredPlan{ID: "plan_a", ProjectID: "p1", Name: "n", Version: v}
		if err := s.SavePlanVersion(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.GetPlan(ctx, "p1", "plan_a", 0)
	if err != nil || got.Version != 3 || got.Latest != 3 {
		t.Fatalf("latest: %+v %v", got, err)
	}

	old, err := s.GetPlan(ctx, "p1", "plan_a", 1)
	if err != nil || old.Version != 1 || old.Latest != 3 {
		t.Fatalf("v1: %+v %v", old, err)
	}

	vs, err := s.ListPlanVersions(ctx, "p1", "plan_a")
	if err != nil || len(vs) != 3 || vs[0].Version != 1 {
		t.Fatalf("versions: %+v %v", vs, err)
	}

	err = s.SavePlanVersion(ctx, &domain.StoredPlan{ID: "plan_a", ProjectID: "p1", Version: 2})
	if err == nil {
		t.Error("an existing version must not be overwritten")
	}
}

func TestPlans_ListDeleteAndNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newPlanStore(t)

	if list, err := s.ListPlans(ctx, "p1"); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}

	_ = s.SavePlanVersion(ctx, &domain.StoredPlan{ID: "plan_a", ProjectID: "p1", Version: 1})
	_ = s.SavePlanVersion(ctx, &domain.StoredPlan{ID: "plan_b", ProjectID: "p1", Version: 1})
	_ = s.SavePlanVersion(ctx, &domain.StoredPlan{ID: "plan_a", ProjectID: "p2", Version: 1})

	list, err := s.ListPlans(ctx, "p1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}

	if err := s.DeletePlan(ctx, "p1", "plan_a"); err != nil {
		t.Fatal(err)
	}

	_, err = s.GetPlan(ctx, "p1", "plan_a", 0)
	if !errors.Is(err, domain.ErrLoadPlanNotFound) || !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleted plan: %v", err)
	}

	if _, err := s.GetPlan(ctx, "p2", "plan_a", 0); err != nil {
		t.Errorf("another project's plan must stay: %v", err)
	}

	if err := s.DeletePlan(ctx, "p1", "plan_a"); !errors.Is(err, domain.ErrLoadPlanNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestPlans_RejectsPathTricks(t *testing.T) {
	t.Parallel()

	s := newPlanStore(t)

	for _, id := range []string{"..", "../x", "a/b", ""} {
		if _, err := s.GetPlan(context.Background(), "p1", id, 0); !errors.Is(err, domain.ErrLoadPlanID) {
			t.Errorf("%q: %v", id, err)
		}
	}
}
