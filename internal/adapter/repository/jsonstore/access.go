package jsonstore

import (
	"slices"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/idgen"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Bounds of the audit log: per project and in total.
const (
	maxAuditPerProject = 2000
	maxAuditTotal      = 20000
)

// dropAccess removes the tokens and share links of a deleted project; the
// audit log keeps its history. The caller holds the lock.
func (s *Store) dropAccess(projectID string) {
	s.d.Tokens = slices.DeleteFunc(s.d.Tokens, func(t domain.AccessToken) bool { return t.ProjectID == projectID })
	s.d.ShareLinks = slices.DeleteFunc(s.d.ShareLinks, func(l domain.ShareLink) bool { return l.ProjectID == projectID })
}

// ---------- usecase.AccessRepository: tokens ----------.

func (s *Store) AddToken(t *domain.AccessToken) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.d.Tokens = append(s.d.Tokens, *t)
	_ = s.save()
}

func (s *Store) FindToken(hash string) (domain.AccessToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.d.Tokens {
		if s.d.Tokens[i].Hash == hash {
			return s.d.Tokens[i], true
		}
	}

	return domain.AccessToken{}, false
}

func (s *Store) GetToken(projectID, id string) (domain.AccessToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.d.Tokens {
		if s.d.Tokens[i].ID == id && s.d.Tokens[i].ProjectID == projectID {
			return s.d.Tokens[i], nil
		}
	}

	return domain.AccessToken{}, domain.ErrNotFound
}

func (s *Store) UpdateToken(t *domain.AccessToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.d.Tokens {
		if s.d.Tokens[i].ID == t.ID {
			s.d.Tokens[i] = *t

			return s.save()
		}
	}

	return domain.ErrNotFound
}

func (s *Store) TouchToken(id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.d.Tokens {
		if s.d.Tokens[i].ID == id {
			s.d.Tokens[i].LastUsedAt = &at
			_ = s.save()

			return
		}
	}
}

func (s *Store) ListTokens(projectID string) []domain.AccessToken {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.AccessToken, 0)

	for i := range s.d.Tokens {
		if s.d.Tokens[i].ProjectID == projectID {
			out = append(out, s.d.Tokens[i])
		}
	}

	return out
}

// ---------- usecase.AccessRepository: share links ----------.

func (s *Store) AddShareLink(l *domain.ShareLink) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.d.ShareLinks = append(s.d.ShareLinks, *l)
	_ = s.save()
}

func (s *Store) FindShareLink(hash string) (domain.ShareLink, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.d.ShareLinks {
		if s.d.ShareLinks[i].Hash == hash {
			return s.d.ShareLinks[i], true
		}
	}

	return domain.ShareLink{}, false
}

func (s *Store) GetShareLink(projectID, id string) (domain.ShareLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.d.ShareLinks {
		if s.d.ShareLinks[i].ID == id && s.d.ShareLinks[i].ProjectID == projectID {
			return s.d.ShareLinks[i], nil
		}
	}

	return domain.ShareLink{}, domain.ErrNotFound
}

func (s *Store) UpdateShareLink(l *domain.ShareLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.d.ShareLinks {
		if s.d.ShareLinks[i].ID == l.ID {
			s.d.ShareLinks[i] = *l

			return s.save()
		}
	}

	return domain.ErrNotFound
}

func (s *Store) ListShareLinks(projectID string) []domain.ShareLink {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.ShareLink, 0)

	for i := range s.d.ShareLinks {
		if s.d.ShareLinks[i].ProjectID == projectID {
			out = append(out, s.d.ShareLinks[i])
		}
	}

	return out
}

// ---------- usecase.AccessRepository: audit log ----------.

func (s *Store) AddAudit(e *domain.AuditEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.ID = idgen.New()
	s.d.Audit = append(s.d.Audit, *e)

	if len(s.d.Audit) > maxAuditTotal {
		s.d.Audit = s.d.Audit[len(s.d.Audit)-maxAuditTotal:]
	}

	s.trimAudit(e.ProjectID)
	_ = s.save()
}

// trimAudit drops the oldest entries of a project beyond its bound. The
// caller holds the lock.
func (s *Store) trimAudit(projectID string) {
	count := 0

	for i := range s.d.Audit {
		if s.d.Audit[i].ProjectID == projectID {
			count++
		}
	}

	excess := count - s.auditCap
	if excess <= 0 {
		return
	}

	s.d.Audit = slices.DeleteFunc(s.d.Audit, func(e domain.AuditEntry) bool {
		if e.ProjectID != projectID || excess == 0 {
			return false
		}

		excess--

		return true
	})
}

func (s *Store) ListAudit(projectID string, q *usecase.AuditQuery) []domain.AuditEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.AuditEntry, 0)

	for i := len(s.d.Audit) - 1; i >= 0 && (q.Limit <= 0 || len(out) < q.Limit); i-- {
		if s.d.Audit[i].ProjectID == projectID && q.Matches(&s.d.Audit[i]) {
			out = append(out, s.d.Audit[i])
		}
	}

	return out
}
