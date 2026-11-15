package loadstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

var versionFileRe = regexp.MustCompile(`^v(\d+)\.json$`)

func (s *Store) planDir(projectID, planID string) (string, error) {
	if !validName(projectID) || !validName(planID) {
		return "", fmt.Errorf("%w: %q", domain.ErrLoadPlanID, planID)
	}

	return filepath.Join(s.plansDir, projectID, planID), nil
}

// SavePlanVersion writes one version of a plan; an existing version is never
// overwritten.
func (s *Store) SavePlanVersion(_ context.Context, p *domain.StoredPlan) error {
	dir, err := s.planDir(p.ProjectID, p.ID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = os.MkdirAll(dir, dirPerm)
	if err != nil {
		return fmt.Errorf("create plan dir: %w", err)
	}

	path := filepath.Join(dir, "v"+strconv.Itoa(p.Version)+".json")

	_, statErr := os.Stat(path)
	if statErr == nil {
		return fmt.Errorf("%w: %s version %d exists", ErrBadName, p.ID, p.Version)
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plan: %w", err)
	}

	return writeAtomic(path, data)
}

// versionsOf returns the version numbers on disk, ascending.
func versionsOf(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrLoadPlanNotFound
		}

		return nil, fmt.Errorf("read plan dir: %w", err)
	}

	var out []int

	for _, e := range entries {
		m := versionFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}

		n, convErr := strconv.Atoi(m[1])
		if convErr == nil {
			out = append(out, n)
		}
	}

	if len(out) == 0 {
		return nil, domain.ErrLoadPlanNotFound
	}

	sort.Ints(out)

	return out, nil
}

func readPlanVersion(dir string, version int) (*domain.StoredPlan, error) {
	data, err := os.ReadFile(filepath.Join(dir, "v"+strconv.Itoa(version)+".json")) //nolint:gosec // dir is validated.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrLoadPlanNotFound
		}

		return nil, fmt.Errorf("read plan: %w", err)
	}

	p := new(domain.StoredPlan)

	err = json.Unmarshal(data, p)
	if err != nil {
		return nil, fmt.Errorf("decode plan: %w", err)
	}

	return p, nil
}

// GetPlan returns a version; 0 is the newest.
func (s *Store) GetPlan(_ context.Context, projectID, planID string, version int) (*domain.StoredPlan, error) {
	dir, err := s.planDir(projectID, planID)
	if err != nil {
		return nil, err
	}

	versions, err := versionsOf(dir)
	if err != nil {
		return nil, err
	}

	latest := versions[len(versions)-1]
	if version == 0 {
		version = latest
	}

	p, err := readPlanVersion(dir, version)
	if err != nil {
		return nil, err
	}

	p.Latest = latest

	return p, nil
}

// ListPlans returns the newest version of each plan, most recently saved first.
func (s *Store) ListPlans(ctx context.Context, projectID string) ([]domain.StoredPlan, error) {
	if !validName(projectID) {
		return nil, fmt.Errorf("%w: %q", domain.ErrLoadPlanID, projectID)
	}

	entries, err := os.ReadDir(filepath.Join(s.plansDir, projectID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []domain.StoredPlan{}, nil
		}

		return nil, fmt.Errorf("read plans: %w", err)
	}

	out := make([]domain.StoredPlan, 0, len(entries))

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		p, getErr := s.GetPlan(ctx, projectID, e.Name(), 0)
		if getErr == nil {
			out = append(out, *p)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	return out, nil
}

// ListPlanVersions returns every version, oldest first.
func (s *Store) ListPlanVersions(ctx context.Context, projectID, planID string) ([]domain.StoredPlan, error) {
	dir, err := s.planDir(projectID, planID)
	if err != nil {
		return nil, err
	}

	versions, err := versionsOf(dir)
	if err != nil {
		return nil, err
	}

	out := make([]domain.StoredPlan, 0, len(versions))

	for _, v := range versions {
		p, getErr := s.GetPlan(ctx, projectID, planID, v)
		if getErr != nil {
			return nil, getErr
		}

		out = append(out, *p)
	}

	return out, nil
}

// DeletePlan removes a plan with all its versions.
func (s *Store) DeletePlan(_ context.Context, projectID, planID string) error {
	dir, err := s.planDir(projectID, planID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, statErr := os.Stat(dir)
	if statErr != nil {
		return domain.ErrLoadPlanNotFound
	}

	err = os.RemoveAll(dir)
	if err != nil {
		return fmt.Errorf("delete plan: %w", err)
	}

	return nil
}
