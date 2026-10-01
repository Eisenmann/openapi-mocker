package usecase_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// fakeStateStore is an in-memory usecase.StateStore (modes share a namespace).
type fakeStateStore struct {
	mu   sync.Mutex
	data map[string]*domain.StateCollection
}

func newFakeStateStore() *fakeStateStore {
	return &fakeStateStore{data: map[string]*domain.StateCollection{}}
}

func (f *fakeStateStore) key(project, collection string) string { return project + "|" + collection }

func (f *fakeStateStore) Transact(
	project string, _ domain.StateMode, collection string, fn func(c *domain.StateCollection) error,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var work domain.StateCollection
	if c := f.data[f.key(project, collection)]; c != nil {
		work = domain.StateCollection{Items: append([]domain.StateItem(nil), c.Items...), NextID: c.NextID}
	}

	if err := fn(&work); err != nil {
		return err
	}

	f.data[f.key(project, collection)] = &work

	return nil
}

func (f *fakeStateStore) View(project string, _ domain.StateMode, collection string) domain.StateCollection {
	f.mu.Lock()
	defer f.mu.Unlock()

	if c := f.data[f.key(project, collection)]; c != nil {
		return domain.StateCollection{Items: append([]domain.StateItem(nil), c.Items...), NextID: c.NextID}
	}

	return domain.StateCollection{}
}

func (f *fakeStateStore) Snapshot(project string, _ domain.StateMode) map[string]domain.StateCollection {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := map[string]domain.StateCollection{}

	for k, c := range f.data {
		if name, ok := strings.CutPrefix(k, project+"|"); ok {
			out[name] = *c
		}
	}

	return out
}

func (f *fakeStateStore) Replace(project string, _ domain.StateMode, collection string, c *domain.StateCollection) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.data[f.key(project, collection)] = c

	return nil
}

func (f *fakeStateStore) Reset(project, collection string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for k := range f.data {
		if collection == "" && strings.HasPrefix(k, project+"|") || k == f.key(project, collection) {
			delete(f.data, k)
		}
	}

	return nil
}

// stateFixture serves a contract whose operation templates are chosen per
// call through the engine stub. The returned serve func answers one request.
type stateFixture struct {
	svc    *usecase.MockServingService
	state  *usecase.StateService
	engine *mockContractEngine
	mocks  *mockMockRepo
	pid    string
}

func newStateFixture(t *testing.T, mode domain.StateMode) *stateFixture {
	t.Helper()

	projects := newMockProjectRepo()
	p := projects.Create("x", "")

	if _, err := projects.SetStateMode(p.ID, mode); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	contracts := newMockContractRepo()
	contracts.AddVersion(p.ID, "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	engine := &mockContractEngine{found: true, exampleBody: []byte(`{"static":true}`)}
	state := usecase.NewStateService(newFakeStateStore(), projects, engine)
	svc := usecase.NewMockServingService(
		contracts, mocks, newMockLogRepo(), engine,
		usecase.WithProjects(projects), usecase.WithState(state),
	)

	return &stateFixture{svc: svc, state: state, engine: engine, mocks: mocks, pid: p.ID}
}

func (f *stateFixture) do(template, method, path, body string) usecase.MockResponse {
	f.engine.pathTemplate = template

	return f.svc.ServeRequest(f.pid, &usecase.MockRequest{
		Method: method, Path: path, Body: []byte(body), SkipValidation: true,
	})
}

func TestState_CreateThenFetch(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateMemory)
	f.engine.successStatus = 201

	created := f.do("/users", "POST", "/users", `{"name":"ann"}`)
	if created.StatusCode != 201 || created.Source != "state" {
		t.Fatalf("create: got %d from %q: %s", created.StatusCode, created.Source, created.Body)
	}

	if !strings.Contains(string(created.Body), `"id":1`) || !strings.Contains(string(created.Body), `"name":"ann"`) {
		t.Errorf("created body %s lacks generated id or data", created.Body)
	}

	got := f.do("/users/{id}", "GET", "/users/1", "")
	if got.StatusCode != 200 || string(got.Body) != string(created.Body) {
		t.Errorf("fetch: got %d %s", got.StatusCode, got.Body)
	}

	list := f.do("/users", "GET", "/users", "")
	if list.StatusCode != 200 || string(list.Body) != "["+string(created.Body)+"]" {
		t.Errorf("list: got %d %s", list.StatusCode, list.Body)
	}

	second := f.do("/users", "POST", "/users", `{"name":"bob"}`)
	if !strings.Contains(string(second.Body), `"id":2`) {
		t.Errorf("second resource must get id 2, got %s", second.Body)
	}
}

func TestState_StringIDsAreUUIDs(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateMemory)
	f.engine.schemaJSON = `{"type":"object","properties":{"id":{"type":"string"}}}`

	resp := f.do("/users", "POST", "/users", `{}`)

	var got map[string]string
	if err := json.Unmarshal(resp.Body, &got); err != nil || len(got["id"]) != 36 {
		t.Errorf("want a UUID id, got %s (%v)", resp.Body, err)
	}
}

func TestState_PutPatchDelete(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateMemory)
	f.do("/users", "POST", "/users", `{"name":"ann","age":30}`)

	put := f.do("/users/{id}", "PUT", "/users/1", `{"name":"anna","id":99}`)
	if put.StatusCode != 200 || string(put.Body) != `{"id":1,"name":"anna"}` {
		t.Errorf("put replaces the resource and keeps its id: got %d %s", put.StatusCode, put.Body)
	}

	patch := f.do("/users/{id}", "PATCH", "/users/1", `{"age":31}`)
	if string(patch.Body) != `{"age":31,"id":1,"name":"anna"}` {
		t.Errorf("patch merges fields: got %s", patch.Body)
	}

	del := f.do("/users/{id}", "DELETE", "/users/1", "")
	if del.StatusCode != 204 || len(del.Body) != 0 {
		t.Errorf("delete: got %d %q", del.StatusCode, del.Body)
	}

	if gone := f.do("/users/{id}", "GET", "/users/1", ""); gone.StatusCode != 404 {
		t.Errorf("deleted resource must be 404, got %d", gone.StatusCode)
	}

	if again := f.do("/users/{id}", "DELETE", "/users/1", ""); again.StatusCode != 404 {
		t.Errorf("second delete must be 404, got %d", again.StatusCode)
	}

	if miss := f.do("/users/{id}", "PUT", "/users/7", `{}`); miss.StatusCode != 404 {
		t.Errorf("put on a missing resource must be 404, got %d", miss.StatusCode)
	}
}

func TestState_ExplicitIDsAndConflict(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateMemory)

	if r := f.do("/users", "POST", "/users", `{"id":5}`); r.StatusCode != 201 {
		t.Fatalf("create with id: %d %s", r.StatusCode, r.Body)
	}

	if r := f.do("/users", "POST", "/users", `{"id":5}`); r.StatusCode != 409 {
		t.Errorf("duplicate id must be 409, got %d", r.StatusCode)
	}

	next := f.do("/users", "POST", "/users", `{}`)
	if !strings.Contains(string(next.Body), `"id":6`) {
		t.Errorf("generated id must follow explicit ids, got %s", next.Body)
	}

	if r := f.do("/users", "POST", "/users", `[1]`); r.StatusCode != 400 {
		t.Errorf("non-object body must be 400, got %d", r.StatusCode)
	}
}

func TestState_NestedCollectionsAreSeparate(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateMemory)
	f.do("/orgs/{org}/users", "POST", "/orgs/a/users", `{"n":1}`)

	if r := f.do("/orgs/{org}/users", "GET", "/orgs/b/users", ""); string(r.Body) != "[]" {
		t.Errorf("org b must be empty, got %s", r.Body)
	}

	if r := f.do("/orgs/{org}/users/{id}", "GET", "/orgs/a/users/1", ""); r.StatusCode != 200 {
		t.Errorf("nested item: got %d", r.StatusCode)
	}
}

func TestState_OffAndRulesWin(t *testing.T) {
	t.Parallel()

	off := newStateFixture(t, domain.StateOff)
	if r := off.do("/users", "GET", "/users", ""); r.Source != "schema-example" {
		t.Errorf("state off must serve the static mock, got %q", r.Source)
	}

	f := newStateFixture(t, domain.StateMemory)
	f.mocks.CreateMock(&domain.MockRule{
		ProjectID: f.pid, Path: "/users", Method: "GET", StatusCode: 200, Body: `{"rule":true}`,
	})

	if r := f.do("/users", "GET", "/users", ""); r.Source != "rule" {
		t.Errorf("an explicit mock rule must win over state, got %q", r.Source)
	}

	if r := f.do("/users", "PUT", "/users", "{}"); r.Source != "schema-example" {
		t.Errorf("operations outside the conventions stay static, got %q", r.Source)
	}
}

func TestState_Admin(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateMemory)

	n, err := f.state.Seed(f.pid, "users", []byte(`[{"id":3,"name":"a"},{"name":"b"}]`))
	if err != nil || n != 2 {
		t.Fatalf("seed: %d %v", n, err)
	}

	if r := f.do("/users/{id}", "GET", "/users/3", ""); r.StatusCode != 200 {
		t.Errorf("seeded resource: got %d", r.StatusCode)
	}

	snap, err := f.state.Snapshot(f.pid)
	if err != nil || len(snap["/users"]) != 2 {
		t.Errorf("snapshot: %v %v", snap, err)
	}

	if _, err := f.state.Seed(f.pid, "users", []byte(`{"a":1}`)); err == nil {
		t.Error("seeding a non-array must fail")
	}

	if _, err := f.state.Seed(f.pid, "users", []byte(`[{"id":1},{"id":1}]`)); err == nil {
		t.Error("seeding duplicate ids must fail")
	}

	if err := f.state.Reset(f.pid, "/users"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	if r := f.do("/users", "GET", "/users", ""); string(r.Body) != "[]" {
		t.Errorf("reset collection must be empty, got %s", r.Body)
	}
}

func TestState_AdminRequiresMode(t *testing.T) {
	t.Parallel()

	f := newStateFixture(t, domain.StateOff)

	if _, err := f.state.Snapshot(f.pid); err == nil {
		t.Error("snapshot must fail when state is off")
	}

	if _, err := f.state.Seed(f.pid, "users", []byte(`[]`)); err == nil {
		t.Error("seed must fail when state is off")
	}
}

func TestProjectService_SetStateMode(t *testing.T) {
	t.Parallel()

	projects := newMockProjectRepo()
	p := projects.Create("x", "")
	svc := usecase.NewProjectService(projects)

	for _, mode := range []domain.StateMode{domain.StateOff, domain.StateMemory, domain.StatePersisted} {
		got, err := svc.SetStateMode(p.ID, mode)
		if err != nil || got.StateMode != mode {
			t.Errorf("mode %q: got %v %v", mode, got, err)
		}
	}

	if got, err := svc.SetStateMode(p.ID, ""); err != nil || got.StateMode != domain.StateOff {
		t.Errorf("empty mode must mean off, got %v %v", got, err)
	}

	if _, err := svc.SetStateMode(p.ID, "sql"); err == nil {
		t.Error("invalid mode must fail")
	}
}
