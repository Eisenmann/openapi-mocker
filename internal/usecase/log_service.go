package usecase

import (
	"errors"
	"io"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// ErrLogNotFound means the request log entry does not exist (any more).
var ErrLogNotFound = errors.New("log entry not found")

// ErrObservabilityOff means the logs are not backed by a LogHub.
var ErrObservabilityOff = errors.New("live logs and metrics are not enabled")

type LogService struct {
	repo LogRepository
	hub  *LogHub // nil when repo is a plain repository.
}

// NewLogService serves request logs from repo. When repo is a *LogHub the
// service also offers live streams, hit statistics and metrics.
func NewLogService(repo LogRepository) *LogService {
	s := &LogService{repo: repo, hub: nil}

	if hub, ok := repo.(*LogHub); ok {
		s.hub = hub
	}

	return s
}

func (s *LogService) List(projectID string, limit int) []*domain.RequestLog {
	return s.repo.ListLogs(projectID, limit)
}

// Detail returns one entry with its captured bodies (while they are in memory).
func (s *LogService) Detail(projectID, id string) (*domain.RequestLog, error) {
	if s.hub == nil {
		return nil, ErrObservabilityOff
	}

	l, ok := s.hub.Get(projectID, id)
	if !ok {
		return nil, ErrLogNotFound
	}

	return l, nil
}

// Subscribe opens a live stream; see LogHub.Subscribe.
func (s *LogService) Subscribe(projectID, afterID string) (*Subscription, []*domain.RequestLog, error) {
	if s.hub == nil {
		return nil, nil, ErrObservabilityOff
	}

	return s.hub.Subscribe(projectID, afterID)
}

// Stats returns the per-endpoint hit counts of a project.
func (s *LogService) Stats(projectID string) (ProjectStats, error) {
	if s.hub == nil {
		return ProjectStats{}, ErrObservabilityOff
	}

	return s.hub.Stats(projectID), nil
}

// ResetStats clears the hit counts of a project.
func (s *LogService) ResetStats(projectID string) error {
	if s.hub == nil {
		return ErrObservabilityOff
	}

	s.hub.ResetStats(projectID)

	return nil
}

// CapturePolicy reports how bodies are captured.
func (s *LogService) CapturePolicy() CapturePolicy {
	if s.hub == nil {
		return CapturePolicy{Enabled: false, MaxBytes: 0}
	}

	return s.hub.Policy()
}

// WriteMetrics renders the Prometheus metrics.
func (s *LogService) WriteMetrics(w io.Writer) error {
	if s.hub == nil {
		return ErrObservabilityOff
	}

	return s.hub.WritePrometheus(w)
}

// Info reports the logging mode and sinks.
func (s *LogService) Info() LogInfo {
	if s.hub == nil {
		return LogInfo{Mode: "local", Sinks: []string{}, ExportBodies: false}
	}

	return s.hub.Info()
}

// CloseStreams ends all live streams; see LogHub.CloseStreams.
func (s *LogService) CloseStreams() {
	if s.hub != nil {
		s.hub.CloseStreams()
	}
}
