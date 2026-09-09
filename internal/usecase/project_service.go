package usecase

import (
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

type ProjectService struct {
	repo ProjectRepository
}

func NewProjectService(repo ProjectRepository) *ProjectService {
	return &ProjectService{repo: repo}
}

func (s *ProjectService) Create(name, description string) (*domain.Project, error) {
	if strings.TrimSpace(name) == "" {
		return nil, ErrProjectNameRequired
	}

	return s.repo.Create(name, description), nil
}

func (s *ProjectService) List() []*domain.Project {
	return s.repo.List()
}

func (s *ProjectService) Get(id string) (*domain.Project, error) {
	return s.repo.Get(id)
}

func (s *ProjectService) Delete(id string) error {
	return s.repo.Delete(id)
}
