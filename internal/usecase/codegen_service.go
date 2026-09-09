package usecase

import "fmt"

type CodegenService struct {
	contracts ContractRepository
	generator CodeGenerator
}

func NewCodegenService(contracts ContractRepository, generator CodeGenerator) *CodegenService {
	return &CodegenService{contracts: contracts, generator: generator}
}

func (s *CodegenService) GenerateServerZip(projectID string) ([]byte, error) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return nil, fmt.Errorf("contract not loaded: %w", err)
	}

	return s.generator.GenerateServerZip([]byte(c.Raw), "generatedserver")
}

func (s *CodegenService) GenerateClientZip(projectID string) ([]byte, error) {
	c, err := s.contracts.GetActive(projectID)
	if err != nil {
		return nil, fmt.Errorf("contract not loaded: %w", err)
	}

	return s.generator.GenerateClientZip([]byte(c.Raw), "generatedclient")
}
