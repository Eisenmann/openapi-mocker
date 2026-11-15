package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/loadstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// newLoadAccessEnv is newAccessEnv with the load testing services wired to a
// fake engine.
func newLoadAccessEnv(t *testing.T) *accessEnv {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	files, err := loadstore.New(t.TempDir(), &loadstore.Options{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runs, err := usecase.NewLoadRunService(ctx, &usecase.LoadRunDeps{
		Engine: loadFakeEngine{}, Store: files, MockBase: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Runs save their result after they end; wait for that before the temp dirs go.
	t.Cleanup(func() { _ = runs.Shutdown(context.Background()) })

	engine := openapi.NewEngine()
	projects := usecase.NewProjectService(store)

	h := httpapi.NewRouter(&httpapi.Services{
		Projects:    projects,
		Contracts:   usecase.NewContractService(store, store, engine, nil, usecase.FormatValidators{}),
		Mocks:       usecase.NewMockService(store, store, store, engine, nil),
		Auth:        usecase.NewAuthService(store, engine, usecase.WithAuthLogs(store)),
		MockServing: usecase.NewMockServingService(store, store, store, engine, usecase.WithProjects(store)),
		Access:      usecase.NewAccessService(store, store, store, usecase.WithAdminToken(adminEnvToken)),
		LoadPlans:   usecase.NewLoadPlanService(files),
		LoadRuns:    runs,
	})

	p, err := projects.Create("acc", "")
	if err != nil {
		t.Fatal(err)
	}

	return &accessEnv{h: h, store: store, id: p.ID}
}

func TestLoadAccess_Roles(t *testing.T) {
	t.Parallel()

	e := newLoadAccessEnv(t)
	admin := e.mkToken(t, "admin", "")
	runner := e.mkToken(t, "load-runner", admin.Token)
	editor := e.mkToken(t, "editor", admin.Token)
	viewer := e.mkToken(t, "viewer", admin.Token)

	mockPlan := `{"name":"m","target":{"baseUrl":"mock://` + e.id + `"},
"scenarios":{"s":{"weight":1,"steps":["GET /ping"]}},"load":{"model":"iterations","iterations":1,"vus":1}}`

	cases := []struct {
		name         string
		method, path string
		body         string
		token        string
		want         int
	}{
		{"viewer lists plans", http.MethodGet, e.url("/load/plans"), "", viewer.Token, http.StatusOK},
		{"viewer lists runs", http.MethodGet, e.url("/load/runs"), "", viewer.Token, http.StatusOK},
		{"viewer cannot save a plan", http.MethodPost, e.url("/load/plans"), mockPlan, viewer.Token, http.StatusForbidden},
		{"viewer cannot run", http.MethodPost, e.url("/load/runs"), mockPlan, viewer.Token, http.StatusForbidden},
		{"viewer cannot read a report", http.MethodGet, e.url("/load/runs/run_x/report"), "", viewer.Token, http.StatusForbidden},
		{"runner saves a plan", http.MethodPost, e.url("/load/plans"), mockPlan, runner.Token, http.StatusCreated},
		{"editor runs against the mock", http.MethodPost, e.url("/load/runs"), mockPlan, editor.Token, http.StatusAccepted},
		{"locked project needs a token", http.MethodPost, e.url("/load/runs"), mockPlan, "", http.StatusUnauthorized},
	}

	for _, tc := range cases {
		rec := e.do(t, tc.method, tc.path, tc.body, tc.token)
		if rec.Code != tc.want {
			t.Errorf("%s: want %d, got %d %s", tc.name, tc.want, rec.Code, rec.Body.String())
		}
	}
}

func TestLoadAccess_ExternalTargetsAndAudit(t *testing.T) {
	t.Parallel()

	e := newLoadAccessEnv(t)
	admin := e.mkToken(t, "admin", "")
	runner := e.mkToken(t, "load-runner", admin.Token)
	editor := e.mkToken(t, "editor", admin.Token)

	external := `{"name":"ext","target":{"baseUrl":"https://staging.example.com","allowHosts":["staging.example.com"]},
"scenarios":{"s":{"weight":1,"steps":["GET /ping"]}},"load":{"model":"iterations","iterations":1,"vus":1}}`
	notAllowed := strings.Replace(external, `"allowHosts":["staging.example.com"]`, `"allowHosts":["other.example.com"]`, 1)

	if rec := e.do(t, http.MethodPost, e.url("/load/runs"), external, editor.Token); rec.Code != http.StatusForbidden {
		t.Errorf("an editor must not load an external target: %d %s", rec.Code, rec.Body.String())
	}

	if rec := e.do(t, http.MethodPost, e.url("/load/runs"), notAllowed, runner.Token); rec.Code != http.StatusForbidden {
		t.Errorf("a host outside allowHosts: %d %s", rec.Code, rec.Body.String())
	}

	if rec := e.do(t, http.MethodPost, e.url("/load/runs"), external, runner.Token); rec.Code != http.StatusAccepted {
		t.Fatalf("a load-runner may load an allowed external target: %d %s", rec.Code, rec.Body.String())
	}

	actions := map[string]string{}

	for _, en := range e.audit(t, admin.Token, "") {
		actions[en["action"].(string)] = en["actor"].(map[string]any)["name"].(string)
	}

	if actions["load.run.start"] != "load-runner-tok" {
		t.Errorf("the run start is audited with its actor: %v", actions)
	}
}
