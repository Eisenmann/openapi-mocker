package usecase

import (
	"errors"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Sentinel errors for validation failures.
var (
	ErrProviderIDRequired      = errors.New("providerId is required")
	ErrProjectNameRequired     = errors.New("project name is required")
	ErrNameAndTypeRequired     = errors.New("name and type are required")
	ErrContractEmpty           = errors.New("contract is empty")
	ErrProviderAndDescRequired = errors.New("providerId and description are required")
	ErrOperationNotFound       = errors.New("operation not found in contract")
	ErrUnsupportedFormat       = errors.New("unsupported contract format")
	ErrInvalidValidationMode   = errors.New("validationMode must be one of: off, warn, enforce")
	ErrInvalidStateMode        = errors.New("stateMode must be one of: off, memory, persisted")
	ErrStateNotEnabled         = errors.New("stateful mocks are not enabled for this project (set stateMode)")
	ErrSeedNotArray            = errors.New("seed data must be a JSON array of objects")
	ErrSeedInvalid             = errors.New("seed data is invalid")
)

// StatusFromError maps a usecase-layer error to the HTTP status code that both
// the serving services (for request logging) and the HTTP adapter (for the
// response) should use. Keeping the mapping in one place prevents the two
// layers from drifting when new sentinel errors are added.
func StatusFromError(err error) int {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return StatusNotFound
	case errors.Is(err, ErrNotGraphQLContract):
		return StatusBadRequest
	case errors.Is(err, ErrNotMCPContract):
		return StatusBadRequest
	case errors.Is(err, ErrInvalidValidationMode), errors.Is(err, ErrInvalidStateMode),
		errors.Is(err, ErrStateNotEnabled), errors.Is(err, ErrSeedNotArray), errors.Is(err, ErrSeedInvalid):
		return StatusBadRequest
	default:
		return StatusInternalServerError
	}
}
