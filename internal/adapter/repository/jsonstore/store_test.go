package jsonstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func newTestStore(t *testing.T) *jsonstore.Store {
	t.Helper()
	dir := t.TempDir()

	s, err := jsonstore.New(dir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	return s
}

func TestStore_ProjectLifecycle(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// Create.
	p := s.Create("My Project", "A test project")
	if p.ID == "" {
		t.Fatal("expected non-empty project ID")
	}

	if p.Name != "My Project" || p.Description != "A test project" {
		t.Errorf("unexpected project: %+v", p)
	}

	// Get.
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Name != "My Project" {
		t.Errorf("expected name 'My Project', got %q", got.Name)
	}

	// List.
	projects := s.List()
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}

	// Get missing.
	_, err = s.Get("missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Delete.
	if err := s.Delete(p.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = s.Get(p.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete missing.
	if err := s.Delete("missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing delete, got %v", err)
	}
}

func TestStore_ContractLifecycle(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := s.Create("P", "")

	// Add versions.
	c1 := s.AddVersion(p.ID, "yaml", "raw1", "manual")
	if c1.Version != 1 {
		t.Errorf("expected version 1, got %d", c1.Version)
	}

	c2 := s.AddVersion(p.ID, "yaml", "raw2", "manual")
	if c2.Version != 2 {
		t.Errorf("expected version 2, got %d", c2.Version)
	}

	// Get active (latest).
	active, err := s.GetActive(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if active.Raw != "raw2" {
		t.Errorf("expected active raw2, got %q", active.Raw)
	}

	// Get version.
	v1, err := s.GetVersion(p.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if v1.Raw != "raw1" {
		t.Errorf("expected raw1, got %q", v1.Raw)
	}

	// Get missing version.
	_, err = s.GetVersion(p.ID, 99)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// List versions.
	versions := s.ListVersions(p.ID)
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}

	// Get active for missing project.
	_, err = s.GetActive("missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_MockLifecycle(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := s.Create("P", "")

	// Create.
	m := &domain.MockRule{
		ProjectID:  p.ID,
		Method:     "GET",
		Path:       "/users",
		StatusCode: 200,
		Body:       `{"users":[]}`,
	}

	created := s.CreateMock(m)
	if created.ID == "" {
		t.Fatal("expected non-empty mock ID")
	}

	// Get.
	got, err := s.GetMock(created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Path != "/users" {
		t.Errorf("expected path /users, got %q", got.Path)
	}

	// List.
	mocks := s.ListMocks(p.ID)
	if len(mocks) != 1 {
		t.Fatalf("expected 1 mock, got %d", len(mocks))
	}

	// Update.
	created.StatusCode = 201
	if err := s.UpdateMock(created); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ = s.GetMock(created.ID)
	if got.StatusCode != 201 {
		t.Errorf("expected status 201, got %d", got.StatusCode)
	}

	// Update missing.
	if err := s.UpdateMock(&domain.MockRule{ID: "missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Delete.
	if err := s.DeleteMock(created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = s.GetMock(created.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete missing.
	if err := s.DeleteMock("missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing delete, got %v", err)
	}
}

func TestStore_ProviderLifecycle(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := s.Create("P", "")

	// Create.
	prov := &domain.LLMProvider{
		ProjectID: p.ID,
		Name:      "test-provider",
		Type:      "openai",
		BaseURL:   "http://localhost:8080",
	}

	created := s.CreateProvider(prov)
	if created.ID == "" {
		t.Fatal("expected non-empty provider ID")
	}

	// Get.
	got, err := s.GetProvider(created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Name != "test-provider" {
		t.Errorf("expected name, got %q", got.Name)
	}

	// List.
	providers := s.ListProviders(p.ID)
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}

	// Global provider (empty projectID) visible to all projects.
	global := s.CreateProvider(&domain.LLMProvider{Name: "global", Type: "openai"})
	_ = global

	providers = s.ListProviders(p.ID)
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers (project + global), got %d", len(providers))
	}

	// Update.
	created.Name = "renamed"
	if err := s.UpdateProvider(created); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ = s.GetProvider(created.ID)
	if got.Name != "renamed" {
		t.Errorf("expected renamed, got %q", got.Name)
	}

	// Update missing.
	if err := s.UpdateProvider(&domain.LLMProvider{ID: "missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Delete.
	if err := s.DeleteProvider(created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = s.GetProvider(created.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete missing.
	if err := s.DeleteProvider("missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing delete, got %v", err)
	}
}

func TestStore_Logs(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := s.Create("P", "")

	// Add logs.
	for range 3 {
		s.Add(&domain.RequestLog{
			ProjectID:  p.ID,
			Method:     "GET",
			Path:       "/users",
			StatusCode: 200,
		})
	}

	// List logs (newest first).
	logs := s.ListLogs(p.ID, 10)
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}

	// Limit.
	logs = s.ListLogs(p.ID, 2)
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs with limit, got %d", len(logs))
	}

	// Filter by project.
	other := s.Create("Other", "")
	s.Add(&domain.RequestLog{ProjectID: other.ID, Method: "GET", Path: "/other"})

	logs = s.ListLogs(p.ID, 10)
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs for project, got %d", len(logs))
	}
}

func TestStore_Persistence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create store and add data.
	s1, err := jsonstore.New(dir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	p := s1.Create("Persisted", "desc")
	s1.AddVersion(p.ID, "yaml", "raw", "manual")

	// Reopen from same dir.
	s2, err := jsonstore.New(dir)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}

	got, err := s2.Get(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Name != "Persisted" {
		t.Errorf("expected persisted name, got %q", got.Name)
	}

	active, err := s2.GetActive(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if active.Raw != "raw" {
		t.Errorf("expected persisted raw, got %q", active.Raw)
	}
}

func TestStore_DeleteProjectCascades(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := s.Create("P", "")

	// Add contract, mock, and log.
	s.AddVersion(p.ID, "yaml", "raw", "manual")
	s.CreateMock(&domain.MockRule{ProjectID: p.ID, Method: "GET", Path: "/x"})
	s.Add(&domain.RequestLog{ProjectID: p.ID, Method: "GET", Path: "/x"})

	// Delete project.
	err := s.Delete(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Contracts and mocks should be gone.
	if _, err := s.GetActive(p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected contracts to be deleted, got %v", err)
	}

	if mocks := s.ListMocks(p.ID); len(mocks) != 0 {
		t.Errorf("expected mocks to be deleted, got %d", len(mocks))
	}
}

func TestStore_New_InvalidDir(t *testing.T) {
	t.Parallel()

	// Create a file to use as a parent path that can't be a directory.
	dir := t.TempDir()

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// Using a file as a directory parent should fail.
	_, err := jsonstore.New(filepath.Join(filePath, "sub"))
	if err == nil {
		t.Fatal("expected error for invalid dir")
	}
}
