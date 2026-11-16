package mcpcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var (
	errNameRequired    = errors.New("name is required")
	errNothingToChange = errors.New("give validation or stateful: there is nothing to change")
	errContractNeeded  = errors.New("contract is required")
	errContractInvalid = errors.New("the contract is not valid")
	errNoEndpoints     = errors.New("this contract has no REST endpoints to list; GraphQL, MCP and gRPC " +
		"contracts are served by their own protocol")
)

func (s *Server) contractTools() []*tool {
	return []*tool{
		newTool("validate_contract", "Validate a contract",
			"Check a contract without saving it. Returns whether it is valid and what is wrong.", kindRead,
			object(props{keyContract: text("The contract text: OpenAPI, GraphQL SDL, MCP manifest or .proto.")}, keyContract),
			s.validateContract),
		newTool("import_contract", "Import a contract",
			"Save a new version of a project's contract. The endpoints are served at once; the previous version "+
				"stays in the history. An invalid contract is rejected.", kindWrite,
			object(props{
				keyProject:  projectProp(),
				keyContract: text("The contract text."),
			}, keyContract), s.importContract),
		newTool("get_contract", "Get the contract",
			"Return the project's contract text, the active version unless a version is given.", kindRead,
			object(props{
				keyProject: projectProp(),
				keyVersion: number("A version number; the active (latest) one when left out."),
			}), s.getContract),
		newTool("list_endpoints", "List endpoints",
			"List the REST operations of the project's contract: method, path and summary.", kindRead,
			object(props{keyProject: projectProp()}), s.listEndpoints),
	}
}

type validateArgs struct {
	Contract string `json:"contract"`
}

func (s *Server) validateContract(_ context.Context, a *validateArgs) (any, error) {
	if strings.TrimSpace(a.Contract) == "" {
		return nil, errContractNeeded
	}

	res := s.deps.Contracts.Validate(a.Contract)

	out := map[string]any{"valid": res.Valid, "operations": res.OpCount}
	if len(res.Errors) > 0 {
		out["errors"] = res.Errors
	}

	if res.Note != "" {
		out["note"] = res.Note
	}

	return out, nil
}

// invalidContract turns a failed validation into an error that lists every
// problem, so the caller can fix them all at once.
func invalidContract(res usecase.ValidationResult) error {
	if res.Valid {
		return nil
	}

	if len(res.Errors) == 0 {
		return errContractInvalid
	}

	return fmt.Errorf("%w: %s", errContractInvalid, strings.Join(res.Errors, "; "))
}

type importArgs struct {
	Project  string `json:"project"`
	Contract string `json:"contract"`
}

func (s *Server) importContract(_ context.Context, a *importArgs) (any, error) {
	if strings.TrimSpace(a.Contract) == "" {
		return nil, errContractNeeded
	}

	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	err = invalidContract(s.deps.Contracts.Validate(a.Contract))
	if err != nil {
		return nil, err
	}

	c, err := s.deps.Contracts.Publish(p.ID, a.Contract, loadActor) //nolint:contextcheck // the service takes no context.
	if err != nil {
		return nil, fmt.Errorf("import the contract: %w", err)
	}

	out := map[string]any{keyProject: p.ID, keyVersion: c.Version, "mock_url": s.mockURL(p.ID)}
	if eps := s.endpoints(p.ID); len(eps) > 0 {
		out[keyEndpoints] = eps
	}

	return out, nil
}

type getContractArgs struct {
	Project string `json:"project"`
	Version int    `json:"version"`
}

func (s *Server) getContract(_ context.Context, a *getContractArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	var (
		raw     string
		version int
	)

	if a.Version > 0 {
		c, getErr := s.deps.Contracts.GetVersion(p.ID, a.Version)
		if getErr != nil {
			return nil, fmt.Errorf("read version %d: %w", a.Version, getErr)
		}

		raw, version = c.Raw, c.Version
	} else {
		c, getErr := s.deps.Contracts.GetActive(p.ID)
		if getErr != nil {
			return nil, fmt.Errorf("the project has no contract yet: %w", getErr)
		}

		raw, version = c.Raw, c.Version
	}

	return map[string]any{keyProject: p.ID, keyVersion: version, keyContract: raw}, nil
}

func (s *Server) listEndpoints(_ context.Context, a *projectArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	eps, err := s.deps.Contracts.ListEndpoints(p.ID)
	if err != nil {
		return nil, fmt.Errorf("%w (%w)", errNoEndpoints, err)
	}

	return map[string]any{keyProject: p.ID, "mock_url": s.mockURL(p.ID), keyEndpoints: eps}, nil
}
