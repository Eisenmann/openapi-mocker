package usecase

import (
	"context"
	"errors"
	"strings"

	"github.com/example/openapi-mocker/internal/domain"
)

type ProviderService struct {
	repo ProviderRepository
	llm  LLMGateway
}

func NewProviderService(repo ProviderRepository, llm LLMGateway) *ProviderService {
	return &ProviderService{repo: repo, llm: llm}
}

func (s *ProviderService) List(projectID string) []*domain.LLMProvider {
	return s.repo.ListProviders(projectID)
}

func (s *ProviderService) Create(p *domain.LLMProvider) (*domain.LLMProvider, error) {
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Type) == "" {
		return nil, errors.New("name and type are required")
	}
	return s.repo.CreateProvider(p), nil
}

func (s *ProviderService) Update(p *domain.LLMProvider) error {
	return s.repo.UpdateProvider(p)
}

func (s *ProviderService) Delete(id string) error {
	return s.repo.DeleteProvider(id)
}

// Test checks that the provider actually responds.
func (s *ProviderService) Test(ctx context.Context, id string) error {
	p, err := s.repo.GetProvider(id)
	if err != nil {
		return err
	}
	_, err = s.llm.Complete(ctx, p, ChatRequest{
		SystemPrompt: "Answer with a single word.",
		UserPrompt:   "Reply with the word: ok",
		MaxTokens:    10,
	})
	return err
}
