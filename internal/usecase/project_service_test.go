package usecase_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestProjectService_Create(t *testing.T) {
	t.Parallel()

	repo := newMockProjectRepo()
	svc := usecase.NewProjectService(repo)

	p, err := svc.Create("My Project", "A test project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Name != "My Project" {
		t.Errorf("expected name 'My Project', got %q", p.Name)
	}

	if p.Description != "A test project" {
		t.Errorf("expected description 'A test project', got %q", p.Description)
	}
}

func TestProjectService_CreateEmptyName(t *testing.T) {
	t.Parallel()

	repo := newMockProjectRepo()
	svc := usecase.NewProjectService(repo)

	_, err := svc.Create("  ", "desc")
	if err == nil {
		t.Fatal("expected error for empty name")
	}

	if !errors.Is(err, usecase.ErrProjectNameRequired) {
		t.Errorf("expected ErrProjectNameRequired, got %v", err)
	}
}

func TestProjectService_List(t *testing.T) {
	t.Parallel()

	repo := newMockProjectRepo()
	svc := usecase.NewProjectService(repo)

	_, _ = svc.Create("A", "desc")
	_, _ = svc.Create("B", "desc")

	projects := svc.List()
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}

func TestProjectService_Get(t *testing.T) {
	t.Parallel()

	repo := newMockProjectRepo()
	svc := usecase.NewProjectService(repo)

	p, _ := svc.Create("A", "desc")

	got, err := svc.Get(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != p.ID {
		t.Errorf("expected ID %q, got %q", p.ID, got.ID)
	}

	_, err = svc.Get("nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProjectService_Delete(t *testing.T) {
	t.Parallel()

	repo := newMockProjectRepo()
	svc := usecase.NewProjectService(repo)

	p, _ := svc.Create("A", "desc")

	err := svc.Delete(p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = svc.Delete(p.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound on second delete, got %v", err)
	}
}
