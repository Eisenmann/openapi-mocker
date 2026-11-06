package usecase

import "github.com/Eisenmann/openapi-mocker/internal/domain"

// DriftService reads and clears the contract-drift reports of a project.
type DriftService struct {
	repo DriftRepository
}

func NewDriftService(repo DriftRepository) *DriftService {
	return &DriftService{repo: repo}
}

// List returns the project's newest reports first.
func (s *DriftService) List(projectID string, limit int) []*domain.DriftReport {
	return s.repo.ListDrift(projectID, limit)
}

// Clear deletes the project's reports.
func (s *DriftService) Clear(projectID string) {
	s.repo.ClearDrift(projectID)
}
