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

// SetValidationMode changes how the project's incoming mock requests are
// validated against its contract. An empty mode means "off".
func (s *ProjectService) SetValidationMode(id string, mode domain.ValidationMode) (*domain.Project, error) {
	if !mode.Valid() {
		return nil, ErrInvalidValidationMode
	}

	if mode == "" {
		mode = domain.ValidationOff
	}

	return s.repo.SetValidationMode(id, mode)
}

// SetStateMode changes whether the project keeps mock state (stateful
// POST/PUT/PATCH/DELETE) and where. An empty mode means "off".
func (s *ProjectService) SetStateMode(id string, mode domain.StateMode) (*domain.Project, error) {
	if !mode.Valid() {
		return nil, ErrInvalidStateMode
	}

	if mode == "" {
		mode = domain.StateOff
	}

	return s.repo.SetStateMode(id, mode)
}

func (s *ProjectService) Delete(id string) error {
	return s.repo.Delete(id)
}
