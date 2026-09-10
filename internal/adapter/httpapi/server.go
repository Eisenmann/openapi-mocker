// Package httpapi is the interface adapter that translates HTTP requests
// into calls to usecase services and back to JSON responses. It is the only
// package in the application (besides cmd/server and adapter/webui) that
// imports net/http — neither domain nor usecase knows anything about HTTP.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Services is all the usecase services needed by the HTTP layer. They are
// assembled in the composition root (cmd/server/main.go) from concrete adapters.
// CodeGenAgentPort is the interface the HTTP layer needs from the
// code generation agent. It is implemented by *agents.CodeGenerationAgent.
type CodeGenAgentPort interface {
	Execute(ctx context.Context, request *agents.AgentRequest) ([]*ports.GenerationResult, error)
}

type Services struct {
	Projects       *usecase.ProjectService
	Contracts      *usecase.ContractService
	Mocks          *usecase.MockService
	Providers      *usecase.ProviderService
	MockServing    *usecase.MockServingService
	GraphQLServing *usecase.GraphQLServingService
	Codegen        *usecase.CodegenService
	Logs           *usecase.LogService
	CodeGenAgent   CodeGenAgentPort
}

type api struct {
	s Services
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if v != nil {
		err := json.NewEncoder(w).Encode(v)
		if err != nil {
			log.Printf("error encoding response: %v", err)
		}
	}
}

// writeError maps a usecase-layer error to an HTTP status code. domain.ErrNotFound
// is the only error with an explicit "meaning" declared by the usecase layer
// (entity not found); the rest are treated as validation/business-rule errors
// (422) or upstream errors when contacting an LLM (these handlers set the code
// themselves).
func writeError(w http.ResponseWriter, defaultStatus int, err error) {
	status := defaultStatus
	if errors.Is(err, domain.ErrNotFound) {
		status = http.StatusNotFound
	}

	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
