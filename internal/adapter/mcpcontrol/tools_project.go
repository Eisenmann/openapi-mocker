package mcpcontrol

import (
	"context"
	"fmt"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// maxEndpointsShown bounds the endpoints listed in a result.
const maxEndpointsShown = 100

func (s *Server) projectTools() []*tool {
	return []*tool{
		newTool("list_projects", "List projects",
			"List the projects with the address each one is served at.", kindRead,
			object(props{}), s.listProjects),
		newTool("create_project", "Create a project",
			"Create a project, optionally with its contract, and serve it at once. The contract may be OpenAPI "+
				"(YAML or JSON), GraphQL SDL, an MCP manifest (JSON) or a .proto file; a contract that does not "+
				"validate is rejected and no project is created. Returns the mock_url to call.", kindWrite,
			object(props{
				"name":        text("Project name."),
				"description": text("Short description."),
				"contract":    text("The contract text. Leave out to create an empty project and import one later."),
				"validation": choice("Check requests against the contract: off (default), warn (report in the log) "+
					"or enforce (reject with HTTP 400).", "off", "warn", "enforce"),
				"stateful": flag("Let POST, PUT, PATCH and DELETE change what later GETs return."),
			}, "name"), s.createProject),
		newTool("update_project", "Change project settings",
			"Change a project's request validation mode or whether it keeps state.", kindWrite,
			object(props{
				"project":    projectProp(),
				"validation": choice("off, warn or enforce.", "off", "warn", "enforce"),
				"stateful":   flag("Keep state for POST, PUT, PATCH and DELETE (true) or not (false)."),
			}), s.updateProject),
		newTool("delete_project", "Delete a project",
			"Delete a project with its contract versions, mock rules and request log. This cannot be undone.",
			kindDestroy, object(props{"project": projectProp()}), s.deleteProject),
	}
}

type noArgs struct{}

func (s *Server) listProjects(_ context.Context, _ *noArgs) (any, error) {
	all := s.deps.Projects.List()
	out := make([]projectView, 0, len(all))

	for _, p := range all {
		out = append(out, s.view(p))
	}

	return map[string]any{"projects": out}, nil
}

type createProjectArgs struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Contract    string `json:"contract"`
	Validation  string `json:"validation"`
	Stateful    bool   `json:"stateful"`
}

func (s *Server) createProject(_ context.Context, a *createProjectArgs) (any, error) {
	if strings.TrimSpace(a.Name) == "" {
		return nil, errNameRequired
	}

	mode := domain.ValidationMode(a.Validation)
	if !mode.Valid() {
		return nil, usecase.ErrInvalidValidationMode
	}

	if strings.TrimSpace(a.Contract) != "" {
		err := invalidContract(s.deps.Contracts.Validate(a.Contract))
		if err != nil {
			return nil, err
		}
	}

	p, err := s.deps.Projects.Create(a.Name, a.Description)
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}

	err = s.configure(p.ID, a.Contract, mode, a.Stateful) //nolint:contextcheck // the service takes no context.
	if err != nil {
		_ = s.deps.Projects.Delete(p.ID)

		return nil, err
	}

	fresh, getErr := s.deps.Projects.Get(p.ID)
	if getErr != nil {
		fresh = p
	}

	out := map[string]any{"project": s.view(fresh)}
	if eps := s.endpoints(p.ID); len(eps) > 0 {
		out["endpoints"] = eps
	}

	return out, nil
}

// configure applies the optional parts of a new project.
func (s *Server) configure(id, contract string, mode domain.ValidationMode, stateful bool) error {
	if strings.TrimSpace(contract) != "" {
		_, err := s.deps.Contracts.Publish(id, contract, "mcp")
		if err != nil {
			return fmt.Errorf("publish the contract: %w", err)
		}
	}

	if mode != "" {
		_, err := s.deps.Projects.SetValidationMode(id, mode)
		if err != nil {
			return fmt.Errorf("set validation: %w", err)
		}
	}

	if stateful {
		_, err := s.deps.Projects.SetStateMode(id, s.stateMode(true))
		if err != nil {
			return fmt.Errorf("set state: %w", err)
		}
	}

	return nil
}

func (s *Server) stateMode(on bool) domain.StateMode {
	switch {
	case !on:
		return domain.StateOff
	case s.deps.PersistState:
		return domain.StatePersisted
	default:
		return domain.StateMemory
	}
}

type updateProjectArgs struct {
	Project    string `json:"project"`
	Validation string `json:"validation"`
	Stateful   *bool  `json:"stateful"`
}

func (s *Server) updateProject(_ context.Context, a *updateProjectArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	if a.Validation == "" && a.Stateful == nil {
		return nil, errNothingToChange
	}

	if a.Validation != "" {
		_, err = s.deps.Projects.SetValidationMode(p.ID, domain.ValidationMode(a.Validation))
		if err != nil {
			return nil, fmt.Errorf("set validation: %w", err)
		}
	}

	if a.Stateful != nil {
		_, err = s.deps.Projects.SetStateMode(p.ID, s.stateMode(*a.Stateful))
		if err != nil {
			return nil, fmt.Errorf("set state: %w", err)
		}
	}

	fresh, err := s.deps.Projects.Get(p.ID)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}

	return map[string]any{"project": s.view(fresh)}, nil
}

type projectArgs struct {
	Project string `json:"project"`
}

func (s *Server) deleteProject(_ context.Context, a *projectArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	err = s.deps.Projects.Delete(p.ID)
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
	}

	return map[string]any{"deleted": p.ID, "name": p.Name}, nil
}

// endpoints lists the operations of the project's contract. Contracts that are
// not REST have none to list, so a failure is not an error.
func (s *Server) endpoints(projectID string) []usecase.Endpoint {
	eps, err := s.deps.Contracts.ListEndpoints(projectID)
	if err != nil {
		return nil
	}

	if len(eps) > maxEndpointsShown {
		eps = eps[:maxEndpointsShown]
	}

	return eps
}
