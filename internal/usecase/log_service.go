package usecase

import "github.com/example/openapi-mocker/internal/domain"

type LogService struct {
	repo LogRepository
}

func NewLogService(repo LogRepository) *LogService {
	return &LogService{repo: repo}
}

func (s *LogService) List(projectID string, limit int) []*domain.RequestLog {
	return s.repo.ListLogs(projectID, limit)
}
