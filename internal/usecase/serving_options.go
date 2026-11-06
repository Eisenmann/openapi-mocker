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
	upstream UpstreamClient
	drifts   DriftRepository
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

// WithUpstream enables the record/replay proxy and drift detection: projects
// whose proxyMode forwards unmatched requests send them through client, and
// projects with driftCheck on save their comparison reports in drifts.
func WithUpstream(client UpstreamClient, drifts DriftRepository) ServingOption {
	return func(c *servingConfig) {
		c.upstream = client
		c.drifts = drifts
	}
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

// proxyConfig returns the project's proxy settings, or the zero config
// (everything off) when projects are not wired in or the project is gone.
func (c *servingConfig) proxyConfig(projectID string) domain.ProxyConfig {
	var cfg domain.ProxyConfig

	if c.projects == nil || c.upstream == nil {
		return cfg
	}

	p, err := c.projects.Get(projectID)
	if err != nil {
		return cfg
	}

	cfg.UpstreamURL, cfg.Mode, cfg.DriftCheck = p.UpstreamURL, p.ProxyMode, p.DriftCheck

	return cfg
}
