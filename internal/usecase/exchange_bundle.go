package usecase

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// ExportBundle packs the project into a Bundle. With history every contract
// version goes in; otherwise only the active one.
func (s *ExchangeService) ExportBundle(projectID string, history bool) (*Bundle, error) {
	p, err := s.projects.Get(projectID)
	if err != nil {
		return nil, err
	}

	versions := s.contracts.ListVersions(projectID)
	if !history && len(versions) > 1 {
		versions = versions[len(versions)-1:]
	}

	b := &Bundle{
		Format: BundleFormat, Version: BundleVersion, ExportedAt: time.Now().UTC(),
		Project: BundleProject{
			Name: p.Name, Description: p.Description, ValidationMode: p.ValidationMode, StateMode: p.StateMode,
		},
		Contracts: make([]BundleContract, 0, len(versions)),
		Mocks:     []domain.MockRule{},
	}

	for _, v := range versions {
		b.Contracts = append(b.Contracts, BundleContract{
			Format: v.Format, Raw: v.Raw, Source: v.Source, CreatedAt: v.CreatedAt,
		})
	}

	for _, m := range s.mocks.List(projectID) {
		rule := *m
		rule.ID, rule.ProjectID = "", ""
		b.Mocks = append(b.Mocks, rule)
	}

	return b, nil
}

// importBundle creates a project from a bundle file.
func (s *ExchangeService) importBundle(data []byte, name string) (*ImportResult, error) {
	var b Bundle

	err := json.Unmarshal(data, &b)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid bundle: %w", ErrInvalidImport, err)
	}

	if b.Version != BundleVersion {
		return nil, fmt.Errorf("%w: unsupported bundle version %d", ErrInvalidImport, b.Version)
	}

	if len(b.Contracts) > maxBundleVersions || len(b.Mocks) > maxBundleMocks {
		return nil, fmt.Errorf("%w: the bundle is too large", ErrInvalidImport)
	}

	err = s.checkBundleContracts(b.Contracts)
	if err != nil {
		return nil, err
	}

	p, err := s.createProject(name, b.Project.Description, b.Project.Name)
	if err != nil {
		return nil, err
	}

	res := &ImportResult{Project: p, Kind: "", Contracts: 0, Endpoints: 0, Mocks: 0, Warnings: nil}

	err = s.applyBundle(p, &b, res)
	if err != nil {
		return nil, s.fail(p.ID, err)
	}

	updated, err := s.projects.Get(p.ID)
	if err == nil {
		res.Project = updated
	}

	return res, nil
}

// checkBundleContracts validates every OpenAPI version before anything is
// created, so a bad bundle leaves nothing behind.
func (s *ExchangeService) checkBundleContracts(contracts []BundleContract) error {
	for i, c := range contracts {
		if c.Format != FormatJSON && c.Format != FormatYAML {
			continue // GraphQL and MCP contracts are validated by their own engines when published.
		}

		if res := s.service.Validate(c.Raw); !res.Valid {
			return fmt.Errorf("%w: contract version %d is invalid: %v", ErrInvalidImport, i+1, res.Errors)
		}
	}

	return nil
}

func (s *ExchangeService) applyBundle(p *domain.Project, b *Bundle, res *ImportResult) error {
	if b.Project.ValidationMode != "" {
		_, err := s.projects.SetValidationMode(p.ID, b.Project.ValidationMode)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidImport, err)
		}
	}

	if b.Project.StateMode != "" {
		_, err := s.projects.SetStateMode(p.ID, b.Project.StateMode)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidImport, err)
		}
	}

	for _, c := range b.Contracts {
		// Added as they are: history is restored, not republished, so no
		// breaking-change alerts fire for old versions.
		s.contracts.AddVersion(p.ID, c.Format, c.Raw, c.Source)

		res.Contracts++
	}

	for i := range b.Mocks {
		rule := b.Mocks[i]
		rule.ID, rule.ProjectID = "", p.ID
		rule.CreatedAt, rule.UpdatedAt = time.Time{}, time.Time{}

		_, err := s.mocks.Create(&rule)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped mock %s %s: %v", rule.Method, rule.Path, err))

			continue
		}

		res.Mocks++
	}

	endpoints, err := s.service.ListEndpoints(p.ID)
	if err == nil {
		res.Endpoints = len(endpoints)
	}

	return nil
}

// ExportCollection writes the project as a Postman or Insomnia collection.
func (s *ExchangeService) ExportCollection(projectID, format, baseURL string) ([]byte, error) {
	p, err := s.projects.Get(projectID)
	if err != nil {
		return nil, err
	}

	endpoints, err := s.service.ListEndpoints(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: the project has no OpenAPI contract: %w", ErrInvalidExport, err)
	}

	return s.codec.ExportCollection(format, &ExportData{
		Name: p.Name, BaseURL: baseURL, Endpoints: endpoints, Mocks: s.mocks.List(projectID),
	})
}
