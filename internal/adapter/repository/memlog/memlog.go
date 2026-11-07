// Package memlog keeps the request log in memory only. It is the log store of
// distributed installations, where history lives in the external log system
// and the process keeps just a recent window for the UI.
package memlog

import (
	"sync"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/idgen"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// DefaultCapacity is how many entries the store keeps.
const DefaultCapacity = 5000

var _ usecase.LogRepository = (*Store)(nil)

// Store is a bounded in-memory usecase.LogRepository.
type Store struct {
	mu   sync.RWMutex
	cap  int
	logs []*domain.RequestLog
}

// New returns a store that keeps the latest capacity entries.
func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}

	return &Store{mu: sync.RWMutex{}, cap: capacity, logs: nil}
}

// Add assigns the entry an id and stores it.
func (s *Store) Add(l *domain.RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l.ID = idgen.New()
	s.logs = append(s.logs, l)

	if len(s.logs) > 2*s.cap {
		s.logs = append([]*domain.RequestLog(nil), s.logs[len(s.logs)-s.cap:]...)
	}
}

// ListLogs returns up to limit entries of the project, newest first.
func (s *Store) ListLogs(projectID string, limit int) []*domain.RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.RequestLog, 0)

	start := max(0, len(s.logs)-s.cap)
	for i := len(s.logs) - 1; i >= start && len(out) < limit; i-- {
		if s.logs[i].ProjectID == projectID {
			out = append(out, s.logs[i])
		}
	}

	return out
}
