package statestore_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/statestore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

var errRollback = errors.New("rollback")

func add(c *domain.StateCollection, id string) {
	c.Items = append(c.Items, domain.StateItem{ID: id, Data: []byte(`{"id":"` + id + `"}`)})
}

func TestStore_TransactCommitsOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	s, err := statestore.New("")
	if err != nil {
		t.Fatal(err)
	}

	err = s.Transact("p", domain.StateMemory, "/u", func(c *domain.StateCollection) error {
		add(c, "a")

		return errRollback
	})
	if !errors.Is(err, errRollback) || len(s.View("p", domain.StateMemory, "/u").Items) != 0 {
		t.Fatalf("failed transaction must leave no trace: %v", err)
	}

	err = s.Transact("p", domain.StateMemory, "/u", func(c *domain.StateCollection) error {
		add(c, "a")

		return nil
	})
	if err != nil || len(s.View("p", domain.StateMemory, "/u").Items) != 1 {
		t.Fatalf("commit failed: %v", err)
	}
}

func TestStore_ViewIsACopy(t *testing.T) {
	t.Parallel()

	s, _ := statestore.New("")
	_ = s.Transact("p", domain.StateMemory, "/u", func(c *domain.StateCollection) error {
		add(c, "a")

		return nil
	})

	v := s.View("p", domain.StateMemory, "/u")
	v.Items[0].ID = "changed"

	if s.View("p", domain.StateMemory, "/u").Items[0].ID != "a" {
		t.Error("mutating a view must not change the store")
	}
}

func TestStore_PersistedSurvivesRestartMemoryDoesNot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s, err := statestore.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []domain.StateMode{domain.StateMemory, domain.StatePersisted} {
		err = s.Transact("p", mode, "/u", func(c *domain.StateCollection) error {
			add(c, "a")
			c.NextID = 2

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	s2, err := statestore.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := s2.View("p", domain.StatePersisted, "/u")
	if len(got.Items) != 1 || got.NextID != 2 {
		t.Errorf("persisted state must be reloaded, got %+v", got)
	}

	if string(got.Items[0].Data) != `{"id":"a"}` {
		t.Errorf("reloaded data must be compact, got %q", got.Items[0].Data)
	}

	if len(s2.View("p", domain.StateMemory, "/u").Items) != 0 {
		t.Error("memory state must not survive a restart")
	}
}

func TestStore_ResetAndSnapshot(t *testing.T) {
	t.Parallel()

	s, _ := statestore.New(t.TempDir())

	for _, name := range []string{"/a", "/b"} {
		_ = s.Transact("p", domain.StatePersisted, name, func(c *domain.StateCollection) error {
			add(c, "1")

			return nil
		})
	}

	if len(s.Snapshot("p", domain.StatePersisted)) != 2 {
		t.Fatal("snapshot must list both collections")
	}

	if err := s.Reset("p", "/a"); err != nil || len(s.Snapshot("p", domain.StatePersisted)) != 1 {
		t.Fatalf("reset one: %v", err)
	}

	if err := s.Reset("p", ""); err != nil || len(s.Snapshot("p", domain.StatePersisted)) != 0 {
		t.Fatalf("reset all: %v", err)
	}
}

func TestStore_ReplaceIsolatesCaller(t *testing.T) {
	t.Parallel()

	s, _ := statestore.New("")
	c := domain.StateCollection{NextID: 1}
	add(&c, "a")

	if err := s.Replace("p", domain.StateMemory, "/u", &c); err != nil {
		t.Fatal(err)
	}

	c.Items[0].ID = "changed"

	if s.View("p", domain.StateMemory, "/u").Items[0].ID != "a" {
		t.Error("store must keep its own copy")
	}
}
