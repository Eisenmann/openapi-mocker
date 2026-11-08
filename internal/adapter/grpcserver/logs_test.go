package grpcserver_test

import (
	"sync"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// testLogs is an in-memory usecase.LogRepository.
type testLogs struct {
	mu      sync.Mutex
	entries []*domain.RequestLog
}

func (l *testLogs) Add(e *domain.RequestLog) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, e)
}

func (l *testLogs) ListLogs(_ string, _ int) []*domain.RequestLog {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]*domain.RequestLog(nil), l.entries...)
}
