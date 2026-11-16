package mcpcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// OperationLister reads the operations of a contract, to generate load plans.
type OperationLister interface {
	LoadOperations(raw []byte) ([]usecase.LoadOperation, error)
}

// Deps are the services the tools call.
type Deps struct {
	Projects  *usecase.ProjectService
	Contracts *usecase.ContractService
	Mocks     *usecase.MockService
	Logs      *usecase.LogService
	// LoadRuns is nil when load testing is not wired; the load tools are then
	// not offered.
	LoadRuns *usecase.LoadRunService
	// Operations is needed to generate a load plan from the contract.
	Operations OperationLister
	// MockBaseURL is where the mock server listens, for example
	// "http://127.0.0.1:8080"; a project is served under /mock/{id}.
	MockBaseURL string
	// PersistState keeps the state of stateful projects on disk instead of in
	// memory.
	PersistState bool
	// Version is the OpenAPI Mocker version, reported to clients.
	Version string
}

// tool is one MCP tool.
type tool struct {
	name        string
	title       string
	description string
	schema      map[string]any
	// args is the type the arguments are decoded into; a test checks that the
	// schema describes exactly its fields.
	args        reflect.Type
	readOnly    bool
	destructive bool
	run         func(ctx context.Context, raw json.RawMessage) (any, error)
}

// kind says how a tool changes things, for the hints clients show.
type kind int

const (
	kindRead kind = iota
	kindWrite
	kindDestroy
)

// newTool builds a tool whose arguments decode into A. Unknown arguments are
// an error, so a misspelt name is reported instead of silently ignored.
func newTool[A any](
	name, title, description string, k kind, schema map[string]any, fn func(ctx context.Context, a *A) (any, error),
) *tool {
	return &tool{
		name: name, title: title, description: description, schema: schema,
		args: reflect.TypeFor[A](), readOnly: k == kindRead, destructive: k == kindDestroy,
		run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a A

			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()

			err := dec.Decode(&a)
			if err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			return fn(ctx, &a)
		},
	}
}

func (s *Server) buildTools() []*tool {
	tools := s.projectTools()
	tools = append(tools, s.contractTools()...)
	tools = append(tools, s.ruleTools()...)
	tools = append(tools, s.observeTools()...)

	if s.deps.LoadRuns != nil {
		tools = append(tools, s.loadTools()...)
	}

	return tools
}

// ---------- JSON schema helpers ----------.

type props = map[string]any

func object(properties props, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}

	return out
}

func text(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func number(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func flag(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func choice(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func stringMap(description string) map[string]any {
	return map[string]any{
		"type": "object", "description": description, "additionalProperties": map[string]any{"type": "string"},
	}
}

func numbers(description string) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": map[string]any{"type": "integer"}}
}

// projectProp is the argument that names a project, shared by most tools.
func projectProp() map[string]any {
	return text("The project, by id or name. May be left out when there is only one project.")
}

// ---------- project lookup ----------.

var (
	errNoProjects = errors.New("there are no projects yet: create one with create_project")
	errAmbiguous  = errors.New("name the project with the project argument")
	// errUnknownProject starts the message of a project that does not exist.
	errUnknownProject = errors.New("no such project")
)

// project finds a project by id or name. An empty ref means the only project.
func (s *Server) project(ref string) (*domain.Project, error) {
	ref = strings.TrimSpace(ref)
	all := s.deps.Projects.List()

	if ref == "" {
		switch len(all) {
		case 0:
			return nil, errNoProjects
		case 1:
			return all[0], nil
		default:
			return nil, fmt.Errorf("%w; there are %d: %s", errAmbiguous, len(all), describeProjects(all))
		}
	}

	p, err := s.deps.Projects.Get(ref)
	if err == nil {
		return p, nil
	}

	return projectByName(all, ref)
}

// projectByName finds the one project called ref, ignoring case.
func projectByName(all []*domain.Project, ref string) (*domain.Project, error) {
	var named []*domain.Project

	for _, c := range all {
		if strings.EqualFold(c.Name, ref) {
			named = append(named, c)
		}
	}

	switch {
	case len(named) == 1:
		return named[0], nil
	case len(named) > 1:
		return nil, fmt.Errorf("%w: %d projects are named %q (%s)", errAmbiguous, len(named), ref, describeProjects(named))
	case len(all) == 0:
		return nil, errNoProjects
	default:
		return nil, fmt.Errorf("%w %q; there are: %s", errUnknownProject, ref, describeProjects(all))
	}
}

func describeProjects(ps []*domain.Project) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s (id %s)", p.Name, p.ID))
	}

	return strings.Join(parts, ", ")
}

// ---------- shared views ----------.

type projectView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	MockURL         string `json:"mock_url"`
	Validation      string `json:"validation"`
	State           string `json:"state"`
	ContractVersion int    `json:"contract_version"`
}

func (s *Server) view(p *domain.Project) projectView {
	v := projectView{
		ID: p.ID, Name: p.Name, Description: p.Description, MockURL: s.mockURL(p.ID),
		Validation: string(p.ValidationMode), State: string(p.StateMode), ContractVersion: 0,
	}

	if v.Validation == "" {
		v.Validation = string(domain.ValidationOff)
	}

	if v.State == "" {
		v.State = string(domain.StateOff)
	}

	c, err := s.deps.Contracts.GetActive(p.ID)
	if err == nil && c != nil {
		v.ContractVersion = c.Version
	}

	return v
}

func (s *Server) mockURL(projectID string) string {
	return strings.TrimRight(s.deps.MockBaseURL, "/") + "/mock/" + projectID
}

// maxBodyChars bounds a captured body in a tool result, to keep results small.
const maxBodyChars = 2000

func clip(s string) string {
	if len(s) <= maxBodyChars {
		return s
	}

	return s[:maxBodyChars] + "... (cut)"
}
