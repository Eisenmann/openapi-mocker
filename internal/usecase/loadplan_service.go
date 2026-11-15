package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	planIDBytes  = 6
	planIDPrefix = "plan_"
)

// LoadPlanStore keeps stored plans with their versions. The file adapter
// implements it.
type LoadPlanStore interface {
	// SavePlanVersion writes one version; the version number is set by the caller.
	SavePlanVersion(ctx context.Context, p *domain.StoredPlan) error
	// GetPlan returns a version; version 0 is the newest. It returns
	// domain.ErrLoadPlanNotFound for an unknown plan or version.
	GetPlan(ctx context.Context, projectID, planID string, version int) (*domain.StoredPlan, error)
	// ListPlans returns the newest version of each plan of a project, the
	// most recently saved first.
	ListPlans(ctx context.Context, projectID string) ([]domain.StoredPlan, error)
	// ListPlanVersions returns every version of a plan, oldest first.
	ListPlanVersions(ctx context.Context, projectID, planID string) ([]domain.StoredPlan, error)
	// DeletePlan removes a plan with all its versions.
	DeletePlan(ctx context.Context, projectID, planID string) error
}

// LoadPlanService saves and reads plans.
type LoadPlanService struct {
	store LoadPlanStore
	now   func() time.Time
	newID func() string

	// mu makes "read the newest version, write the next" one step.
	mu sync.Mutex
}

// NewLoadPlanService returns a plan service on a store.
func NewLoadPlanService(store LoadPlanStore) *LoadPlanService {
	s := new(LoadPlanService)
	s.store = store
	s.now = time.Now
	s.newID = newPlanID

	return s
}

func newPlanID() string {
	b := make([]byte, planIDBytes)
	_, _ = rand.Read(b)

	return planIDPrefix + hex.EncodeToString(b)
}

// Create stores a plan as version 1 of a new plan.
func (s *LoadPlanService) Create(
	ctx context.Context, projectID string, plan *domain.Plan, actor string,
) (*domain.StoredPlan, error) {
	return s.save(ctx, projectID, s.newID(), 1, plan, actor)
}

// Update stores a plan as the next version of an existing plan.
func (s *LoadPlanService) Update(
	ctx context.Context, projectID, planID string, plan *domain.Plan, actor string,
) (*domain.StoredPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.store.GetPlan(ctx, projectID, planID, 0)
	if err != nil {
		return nil, fmt.Errorf("update load plan: %w", err)
	}

	return s.saveLocked(ctx, projectID, planID, cur.Version+1, plan, actor)
}

func (s *LoadPlanService) save(
	ctx context.Context, projectID, planID string, version int, plan *domain.Plan, actor string,
) (*domain.StoredPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveLocked(ctx, projectID, planID, version, plan, actor)
}

func (s *LoadPlanService) saveLocked(
	ctx context.Context, projectID, planID string, version int, plan *domain.Plan, actor string,
) (*domain.StoredPlan, error) {
	err := plan.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid load plan: %w", err)
	}

	p := new(domain.StoredPlan)
	p.ID = planID
	p.ProjectID = projectID
	p.Name = plan.Name
	p.Version = version
	p.Latest = version
	p.Plan = *plan
	p.CreatedAt = s.now()
	p.CreatedBy = actor

	err = s.store.SavePlanVersion(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("save load plan: %w", err)
	}

	return p, nil
}

// Get returns a version of a plan; version 0 is the newest.
func (s *LoadPlanService) Get(ctx context.Context, projectID, planID string, version int) (*domain.StoredPlan, error) {
	p, err := s.store.GetPlan(ctx, projectID, planID, version)
	if err != nil {
		return nil, fmt.Errorf("get load plan: %w", err)
	}

	return p, nil
}

// List returns the newest version of each plan of the project.
func (s *LoadPlanService) List(ctx context.Context, projectID string) ([]domain.StoredPlan, error) {
	plans, err := s.store.ListPlans(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list load plans: %w", err)
	}

	return plans, nil
}

// Versions returns every version of a plan, oldest first.
func (s *LoadPlanService) Versions(ctx context.Context, projectID, planID string) ([]domain.StoredPlan, error) {
	vs, err := s.store.ListPlanVersions(ctx, projectID, planID)
	if err != nil {
		return nil, fmt.Errorf("list load plan versions: %w", err)
	}

	return vs, nil
}

// Delete removes a plan with all its versions.
func (s *LoadPlanService) Delete(ctx context.Context, projectID, planID string) error {
	err := s.store.DeletePlan(ctx, projectID, planID)
	if err != nil {
		return fmt.Errorf("delete load plan: %w", err)
	}

	return nil
}
