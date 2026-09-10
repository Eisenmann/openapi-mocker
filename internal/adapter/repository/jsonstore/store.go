// Package jsonstore is the persistence adapter (Interface Adapter /
// Frameworks & Drivers in Clean Architecture terms): it implements ALL
// repository ports of the usecase layer (usecase.ProjectRepository,
// ContractRepository, MockRepository, ProviderRepository, LogRepository)
// backed by a single JSON file on disk. No usecase service knows that data
// is stored this way — if desired, Store can be replaced with an
// implementation over Postgres/SQLite by implementing the same 5 interfaces,
// and the composition root (main.go) won't change a single line in the
// usecase layer.
package jsonstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/idgen"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// The compiler checks right here and now that Store implements all 5 ports
// that the usecase layer expects from it — rather than somewhere in main.go
// at the constructor-chain compilation stage, where the error would be far
// less obvious.
var (
	_ usecase.ProjectRepository  = (*Store)(nil)
	_ usecase.ContractRepository = (*Store)(nil)
	_ usecase.MockRepository     = (*Store)(nil)
	_ usecase.ProviderRepository = (*Store)(nil)
	_ usecase.LogRepository      = (*Store)(nil)
)

type data struct {
	Projects  map[string]*domain.Project     `json:"projects"`
	Contracts map[string][]*domain.Contract  `json:"contracts"` // key = projectID, version history.
	Mocks     map[string]*domain.MockRule    `json:"mocks"`
	Providers map[string]*domain.LLMProvider `json:"providers"`
	Logs      []*domain.RequestLog           `json:"logs"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	d    data
}

func New(dir string) (*Store, error) {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return nil, err
	}

	s := &Store{
		path: filepath.Join(dir, "db.json"),
		d: data{
			Projects:  map[string]*domain.Project{},
			Contracts: map[string][]*domain.Contract{},
			Mocks:     map[string]*domain.MockRule{},
			Providers: map[string]*domain.LLMProvider{},
			Logs:      []*domain.RequestLog{},
		},
	}
	err = s.load()
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	return json.Unmarshal(b, &s.d)
}

// save persists state atomically (temp file + rename). It must be called
// while already holding s.mu.
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}

	return os.Rename(tmp, s.path)
}

// ---------- usecase.ProjectRepository ----------.

func (s *Store) Create(name, desc string) *domain.Project {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	p := &domain.Project{ID: idgen.New(), Name: name, Description: desc, CreatedAt: now, UpdatedAt: now}
	s.d.Projects[p.ID] = p
	_ = s.save()

	return p
}

func (s *Store) List() []*domain.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.Project, 0, len(s.d.Projects))
	for _, p := range s.d.Projects {
		out = append(out, p)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	return out
}

func (s *Store) Get(id string) (*domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.d.Projects[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return p, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.d.Projects[id]; !ok {
		return domain.ErrNotFound
	}

	delete(s.d.Projects, id)
	delete(s.d.Contracts, id)

	for k, m := range s.d.Mocks {
		if m.ProjectID == id {
			delete(s.d.Mocks, k)
		}
	}

	return s.save()
}

// ---------- usecase.ContractRepository ----------.

func (s *Store) AddVersion(projectID, format, raw, source string) *domain.Contract {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.d.Contracts[projectID]
	c := &domain.Contract{
		ID:        idgen.New(),
		ProjectID: projectID,
		Format:    format,
		Raw:       raw,
		Version:   len(history) + 1,
		Source:    source,
		CreatedAt: time.Now().UTC(),
	}

	s.d.Contracts[projectID] = append(history, c)
	if p, ok := s.d.Projects[projectID]; ok {
		p.UpdatedAt = c.CreatedAt
	}

	_ = s.save()

	return c
}

func (s *Store) GetActive(projectID string) (*domain.Contract, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.d.Contracts[projectID]
	if len(history) == 0 {
		return nil, domain.ErrNotFound
	}

	return history[len(history)-1], nil
}

func (s *Store) GetVersion(projectID string, version int) (*domain.Contract, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, c := range s.d.Contracts[projectID] {
		if c.Version == version {
			return c, nil
		}
	}

	return nil, domain.ErrNotFound
}

func (s *Store) ListVersions(projectID string) []*domain.Contract {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return append([]*domain.Contract{}, s.d.Contracts[projectID]...)
}

// ---------- usecase.MockRepository ----------.

func (s *Store) CreateMock(m *domain.MockRule) *domain.MockRule {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	m.ID = idgen.New()
	m.CreatedAt = now
	m.UpdatedAt = now
	s.d.Mocks[m.ID] = m
	_ = s.save()

	return m
}

func (s *Store) UpdateMock(m *domain.MockRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.d.Mocks[m.ID]; !ok {
		return domain.ErrNotFound
	}

	m.UpdatedAt = time.Now().UTC()
	s.d.Mocks[m.ID] = m

	return s.save()
}

func (s *Store) DeleteMock(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.d.Mocks[id]; !ok {
		return domain.ErrNotFound
	}

	delete(s.d.Mocks, id)

	return s.save()
}

func (s *Store) ListMocks(projectID string) []*domain.MockRule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.MockRule, 0)

	for _, m := range s.d.Mocks {
		if m.ProjectID == projectID {
			out = append(out, m)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	return out
}

func (s *Store) GetMock(id string) (*domain.MockRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m, ok := s.d.Mocks[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return m, nil
}

// ---------- usecase.ProviderRepository ----------.

func (s *Store) CreateProvider(p *domain.LLMProvider) *domain.LLMProvider {
	s.mu.Lock()
	defer s.mu.Unlock()

	p.ID = idgen.New()
	p.CreatedAt = time.Now().UTC()
	s.d.Providers[p.ID] = p
	_ = s.save()

	return p
}

func (s *Store) UpdateProvider(p *domain.LLMProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.d.Providers[p.ID]; !ok {
		return domain.ErrNotFound
	}

	s.d.Providers[p.ID] = p

	return s.save()
}

func (s *Store) DeleteProvider(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.d.Providers[id]; !ok {
		return domain.ErrNotFound
	}

	delete(s.d.Providers, id)

	return s.save()
}

func (s *Store) ListProviders(projectID string) []*domain.LLMProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.LLMProvider, 0)

	for _, p := range s.d.Providers {
		if p.ProjectID == "" || p.ProjectID == projectID {
			out = append(out, p)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func (s *Store) GetProvider(id string) (*domain.LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.d.Providers[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return p, nil
}

// ---------- usecase.LogRepository ----------.

func (s *Store) Add(l *domain.RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l.ID = idgen.New()
	s.d.Logs = append(s.d.Logs, l)
	// sliding log window so the file doesn't grow forever.
	if len(s.d.Logs) > 5000 {
		s.d.Logs = s.d.Logs[len(s.d.Logs)-5000:]
	}

	_ = s.save()
}

func (s *Store) ListLogs(projectID string, limit int) []*domain.RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.RequestLog, 0)
	for i := len(s.d.Logs) - 1; i >= 0 && len(out) < limit; i-- {
		if s.d.Logs[i].ProjectID == projectID {
			out = append(out, s.d.Logs[i])
		}
	}

	return out
}
