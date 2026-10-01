package usecase

import "github.com/Eisenmann/openapi-mocker/internal/domain"

// ProjectGetter is the slice of ProjectRepository the serving services need
// to look up a project's request-validation mode.
type ProjectGetter interface {
	Get(id string) (*domain.Project, error)
}

// ServingOption configures optional behavior of the mock serving services.
type ServingOption func(*servingConfig)

type servingConfig struct {
	projects ProjectGetter
	state    *StateService
}

// WithProjects enables per-project request validation: the serving service
// reads the project's validation mode from projects on every request.
// Without it, requests are never validated.
func WithProjects(projects ProjectGetter) ServingOption {
	return func(c *servingConfig) { c.projects = projects }
}

// WithState enables stateful mocks: for projects whose stateMode is on, CRUD
// operations that no mock rule covers read and write the project's collections.
func WithState(state *StateService) ServingOption {
	return func(c *servingConfig) { c.state = state }
}

func newServingConfig(opts []ServingOption) servingConfig {
	var cfg servingConfig

	for _, opt := range opts {
		opt(&cfg)
	}

	return cfg
}

// validationMode returns the project's validation mode, or ValidationOff when
// validation is not wired in or the project cannot be loaded.
func (c *servingConfig) validationMode(projectID string) domain.ValidationMode {
	if c.projects == nil {
		return domain.ValidationOff
	}

	p, err := c.projects.Get(projectID)
	if err != nil || p.ValidationMode == "" {
		return domain.ValidationOff
	}

	return p.ValidationMode
}
