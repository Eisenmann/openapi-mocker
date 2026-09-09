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
	default:
		return StatusInternalServerError
	}
}
