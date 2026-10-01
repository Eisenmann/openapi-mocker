package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/statestore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const stateOpenAPI = `openapi: 3.0.0
info: {title: Users, version: 1.0.0}
paths:
  /users:
    get:
      responses:
        '200': {description: OK, content: {application/json: {schema: {type: array, items: {type: object}}}}}
    post:
      requestBody:
        content: {application/json: {schema: {type: object}}}
      responses:
        '201': {description: Created, content: {application/json: {schema: {type: object, properties: {id: {type: integer}}}}}}
  /users/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: integer}}
    get:
      responses:
        '200': {description: OK, content: {application/json: {schema: {type: object}}}}
    put:
      responses:
        '200': {description: OK, content: {application/json: {schema: {type: object}}}}
    delete:
      responses:
        '204': {description: Gone}
`

func newStateRouter(t *testing.T, dir string) (h http.Handler, projectID string) {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	engine := openapi.NewEngine()

	p := projects.Create("s", "")
	contracts.AddVersion(p.ID, "yaml", stateOpenAPI, "manual")

	store, err := statestore.New(dir)
	if err != nil {
		t.Fatalf("state store: %v", err)
	}

	state := usecase.NewStateService(store, projects, engine)
	svc := httpapi.Services{
		Projects: usecase.NewProjectService(projects),
		MockServing: usecase.NewMockServingService(
			contracts, newMemMockRepo(), newMemLogRepo(), engine,
			usecase.WithProjects(projects), usecase.WithState(state),
		),
		State: state,
	}

	return httpapi.NewRouter(&svc), p.ID
}

func call(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, h, method, path, strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
}

func TestRouter_StatefulCreateThenFetch(t *testing.T) {
	t.Parallel()

	h, id := newStateRouter(t, "")

	// State is off by default: the static example is served and nothing is stored.
	if rec := call(t, h, http.MethodPost, "/mock/"+id+"/users", `{"name":"ann"}`); rec.Code != http.StatusOK {
		t.Fatalf("state off: want the static 200, got %d", rec.Code)
	}

	if rec := call(t, h, http.MethodGet, "/api/projects/"+id+"/state", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("state view must be refused while off, got %d", rec.Code)
	}

	if rec := call(t, h, http.MethodPatch, "/api/projects/"+id, `{"stateMode":"memory"}`); rec.Code != http.StatusOK {
		t.Fatalf("enable state: %d %s", rec.Code, rec.Body.String())
	}

	if rec := call(t, h, http.MethodPatch, "/api/projects/"+id, `{"stateMode":"sql"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid stateMode: want 400, got %d", rec.Code)
	}

	rec := call(t, h, http.MethodPost, "/mock/"+id+"/users", `{"name":"ann"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":1`) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	if rec = call(t, h, http.MethodGet, "/mock/"+id+"/users/1", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"name":"ann"`) {
		t.Errorf("fetch: %d %s", rec.Code, rec.Body.String())
	}

	if rec = call(t, h, http.MethodDelete, "/mock/"+id+"/users/1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: want 204, got %d", rec.Code)
	}

	if rec = call(t, h, http.MethodGet, "/mock/"+id+"/users/1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("fetch after delete: want 404, got %d", rec.Code)
	}
}

func TestRouter_StateAdminSeedViewReset(t *testing.T) {
	t.Parallel()

	h, id := newStateRouter(t, "")
	call(t, h, http.MethodPatch, "/api/projects/"+id, `{"stateMode":"persisted"}`)

	if rec := call(t, h, http.MethodPut, "/api/projects/"+id+"/state/users", `[{"id":7,"name":"seed"}]`); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body.String())
	}

	if rec := call(t, h, http.MethodPut, "/api/projects/"+id+"/state/users", `{"x":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("seed with an object: want 400, got %d", rec.Code)
	}

	if rec := call(t, h, http.MethodGet, "/mock/"+id+"/users/7", ""); !strings.Contains(rec.Body.String(), "seed") {
		t.Errorf("seeded resource not served: %s", rec.Body.String())
	}

	rec := call(t, h, http.MethodGet, "/api/projects/"+id+"/state", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/users"`) {
		t.Errorf("state view: %d %s", rec.Code, rec.Body.String())
	}

	if rec = call(t, h, http.MethodDelete, "/api/projects/"+id+"/state", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d", rec.Code)
	}

	if rec = call(t, h, http.MethodGet, "/mock/"+id+"/users", ""); rec.Body.String() != "[]" {
		t.Errorf("collection must be empty after reset, got %s", rec.Body.String())
	}
}

func TestRouter_StatePersistedSurvivesRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	h, id := newStateRouter(t, dir)
	call(t, h, http.MethodPatch, "/api/projects/"+id, `{"stateMode":"persisted"}`)
	call(t, h, http.MethodPost, "/mock/"+id+"/users", `{"name":"kept"}`)

	// A fresh router over the same directory (project ids are deterministic in the stubs).
	h2, id2 := newStateRouter(t, dir)
	if id2 != id {
		t.Fatalf("stub project ids differ: %s vs %s", id, id2)
	}

	call(t, h2, http.MethodPatch, "/api/projects/"+id2, `{"stateMode":"persisted"}`)

	if rec := call(t, h2, http.MethodGet, "/mock/"+id2+"/users/1", ""); !strings.Contains(rec.Body.String(), "kept") {
		t.Errorf("persisted resource lost after restart: %d %s", rec.Code, rec.Body.String())
	}
}
