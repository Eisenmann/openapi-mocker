package usecase

import (
	"fmt"
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

// ProxyPatch is a partial change of a project's proxy settings; nil fields
// keep their current value.
type ProxyPatch struct {
	UpstreamURL *string
	Mode        *domain.ProxyMode
	DriftCheck  *bool
}

// UpdateProxy changes the project's record/replay and drift-detection
// settings and validates the result as a whole.
func (s *ProjectService) UpdateProxy(id string, patch ProxyPatch) (*domain.Project, error) {
	p, err := s.repo.Get(id)
	if err != nil {
		return nil, err
	}

	cfg := domain.ProxyConfig{UpstreamURL: p.UpstreamURL, Mode: p.ProxyMode, DriftCheck: p.DriftCheck}

	if patch.UpstreamURL != nil {
		cfg.UpstreamURL = strings.TrimSpace(*patch.UpstreamURL)
	}

	if patch.Mode != nil {
		cfg.Mode = *patch.Mode
	}

	if patch.DriftCheck != nil {
		cfg.DriftCheck = *patch.DriftCheck
	}

	if cfg.Mode == "" {
		cfg.Mode = domain.ProxyOff
	}

	err = cfg.Validate()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidProxy, err)
	}

	return s.repo.SetProxy(id, cfg)
}

// SetAuth replaces the project's auth simulation after validating it. A nil
// config, or one whose mode is "off", switches the simulation off.
func (s *ProjectService) SetAuth(id string, cfg *domain.AuthConfig) (*domain.Project, error) {
	if cfg != nil {
		err := cfg.Validate()
		if err != nil {
			return nil, err
		}

		if cfg.Mode == "" {
			cfg.Mode = domain.AuthOff
		}
	}

	return s.repo.SetAuth(id, cfg)
}

func (s *ProjectService) Delete(id string) error {
	return s.repo.Delete(id)
}
