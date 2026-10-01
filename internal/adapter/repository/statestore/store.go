// Package statestore is the adapter behind usecase.StateStore: it keeps the
// collections of stateful mocks in memory and, for projects in "persisted"
// mode, mirrors them to a JSON file (state.json) next to the main data file.
package statestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// stateFileName is the file, inside the data directory, holding persisted state.
const stateFileName = "state.json"

// File modes for the state directory and file (owner only).
const (
	dirMode  = 0o750
	fileMode = 0o600
)

var _ usecase.StateStore = (*Store)(nil)

// collections maps project id -> collection name -> collection.
type collections map[string]map[string]*domain.StateCollection

// fileData is the on-disk layout of state.json.
type fileData struct {
	Persisted collections `json:"persisted"`
}

// Store implements usecase.StateStore.
type Store struct {
	mu        sync.RWMutex
	path      string // empty: nothing is written to disk.
	memory    collections
	persisted collections
}

// New returns a store that persists "persisted"-mode collections to
// dir/state.json, loading existing ones. An empty dir disables persistence
// (persisted mode then behaves like memory), which is useful in tests.
func New(dir string) (*Store, error) {
	s := &Store{mu: sync.RWMutex{}, path: "", memory: collections{}, persisted: collections{}}
	if dir == "" {
		return s, nil
	}

	err := os.MkdirAll(dir, dirMode)
	if err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	s.path = filepath.Join(dir, stateFileName)

	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var fd fileData

	err = json.Unmarshal(b, &fd)
	if err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}

	if fd.Persisted != nil {
		compactAll(fd.Persisted)
		s.persisted = fd.Persisted
	}

	return s, nil
}

// compactAll strips the indentation state.json is written with, so resources
// are served exactly as they were stored.
func compactAll(cols collections) {
	for _, byName := range cols {
		for _, c := range byName {
			for i := range c.Items {
				var buf bytes.Buffer

				if json.Compact(&buf, c.Items[i].Data) == nil {
					c.Items[i].Data = buf.Bytes()
				}
			}
		}
	}
}

func clone(c *domain.StateCollection) domain.StateCollection {
	if c == nil {
		return domain.StateCollection{Items: []domain.StateItem{}, NextID: 0}
	}

	items := make([]domain.StateItem, len(c.Items))
	copy(items, c.Items)

	return domain.StateCollection{Items: items, NextID: c.NextID}
}

// Transact implements usecase.StateStore.
func (s *Store) Transact(
	projectID string, mode domain.StateMode, collection string, fn func(c *domain.StateCollection) error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ns := s.namespace(mode)
	work := clone(ns[projectID][collection])

	err := fn(&work)
	if err != nil {
		return err
	}

	s.put(ns, projectID, collection, &work)

	return s.flush(mode)
}

// View implements usecase.StateStore.
func (s *Store) View(projectID string, mode domain.StateMode, collection string) domain.StateCollection {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return clone(s.namespace(mode)[projectID][collection])
}

// Snapshot implements usecase.StateStore.
func (s *Store) Snapshot(projectID string, mode domain.StateMode) map[string]domain.StateCollection {
	s.mu.RLock()
	defer s.mu.RUnlock()

	src := s.namespace(mode)[projectID]
	out := make(map[string]domain.StateCollection, len(src))

	for name, c := range src {
		out[name] = clone(c)
	}

	return out
}

// Replace implements usecase.StateStore.
func (s *Store) Replace(projectID string, mode domain.StateMode, collection string, c *domain.StateCollection) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	work := clone(c)
	s.put(s.namespace(mode), projectID, collection, &work)

	return s.flush(mode)
}

// Reset implements usecase.StateStore.
func (s *Store) Reset(projectID, collection string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ns := range []collections{s.memory, s.persisted} {
		if collection == "" {
			delete(ns, projectID)

			continue
		}

		delete(ns[projectID], collection)
	}

	return s.flush(domain.StatePersisted)
}

func (s *Store) put(ns collections, projectID, collection string, c *domain.StateCollection) {
	if ns[projectID] == nil {
		ns[projectID] = map[string]*domain.StateCollection{}
	}

	ns[projectID][collection] = c
}

// flush writes state.json atomically when the mode is persisted. Callers hold
// s.mu.
func (s *Store) flush(mode domain.StateMode) error {
	if mode != domain.StatePersisted || s.path == "" {
		return nil
	}

	b, err := json.MarshalIndent(fileData{Persisted: s.persisted}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	tmp := s.path + ".tmp"

	err = os.WriteFile(tmp, b, fileMode)
	if err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	err = os.Rename(tmp, s.path)
	if err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}

	return nil
}

// namespace returns the collections of the given mode.
func (s *Store) namespace(mode domain.StateMode) collections {
	if mode == domain.StatePersisted {
		return s.persisted
	}

	return s.memory
}
