package usecase

import "github.com/Eisenmann/openapi-mocker/internal/domain"

// JournalPerProject is how many recent calls of one project the hub keeps,
// with their captured bodies, for test assertions (see LogService.Verify).
const JournalPerProject = 1000

// journal keeps the latest calls of every project, oldest first. It is
// guarded by the LogHub's mutex.
type journal struct {
	byProject map[string][]*domain.RequestLog
}

func newJournal() *journal {
	return &journal{byProject: map[string][]*domain.RequestLog{}}
}

func (j *journal) add(l *domain.RequestLog) {
	calls := j.byProject[l.ProjectID]
	calls = append(calls, l)

	if len(calls) > 2*JournalPerProject {
		calls = append([]*domain.RequestLog(nil), calls[len(calls)-JournalPerProject:]...)
	}

	j.byProject[l.ProjectID] = calls
}

// calls returns a copy of the project's latest calls, oldest first.
func (j *journal) calls(projectID string) []*domain.RequestLog {
	calls := j.byProject[projectID]
	if len(calls) > JournalPerProject {
		calls = calls[len(calls)-JournalPerProject:]
	}

	return append([]*domain.RequestLog(nil), calls...)
}

func (j *journal) clear(projectID string) int {
	n := len(j.calls(projectID))
	delete(j.byProject, projectID)

	return n
}

// Journal returns the project's recent calls with their captured bodies,
// oldest first.
func (h *LogHub) Journal(projectID string) []*domain.RequestLog {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.journal.calls(projectID)
}

// ClearJournal forgets the project's recorded calls and returns how many
// there were. The request log and the hit counts are not touched.
func (h *LogHub) ClearJournal(projectID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.journal.clear(projectID)
}
