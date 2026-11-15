// Package loadstore keeps load runs on disk, one folder per run, so the large
// detail files of a run stay out of the JSON data file.
//
//	DATA_DIR/loadruns/<runId>/run.json
//	DATA_DIR/loadruns/<runId>/summary.json, histograms.bin, ... (artifacts)
package loadstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Environment variables of the retention policy.
const (
	EnvKeep   = "LOAD_RUNS_KEEP"
	EnvMaxAge = "LOAD_RUNS_MAX_AGE"

	// DefaultKeep is how many runs of a project are kept.
	DefaultKeep = 50
	// DefaultMaxAge is how long a finished run is kept.
	DefaultMaxAge = 30 * 24 * time.Hour

	dirName      = "loadruns"
	plansDirName = "loadplans"
	runFile      = "run.json"
	dirPerm      = 0o750
	filePerm     = 0o600
	hoursPerDay  = 24
	interrupted  = "interrupted by a server restart"
)

// Errors of the store.
var (
	ErrBadName     = errors.New("invalid run id or artifact name")
	ErrBadSetting  = errors.New("invalid load run retention setting")
	ErrReservedRun = errors.New("run.json is managed by the store")
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Options is the retention policy; zero means "no limit" for that rule.
type Options struct {
	// Keep is the number of runs kept per project; the oldest are deleted.
	Keep int
	// MaxAge deletes finished runs older than this.
	MaxAge time.Duration
	// Now may be replaced in tests.
	Now func() time.Time
}

// OptionsFromEnv reads LOAD_RUNS_KEEP (a count) and LOAD_RUNS_MAX_AGE (such as
// 720h or 30d). Unset variables get the defaults; 0 switches a rule off.
func OptionsFromEnv(getenv func(string) string) (Options, error) {
	o := Options{Keep: DefaultKeep, MaxAge: DefaultMaxAge, Now: nil}

	if raw := getenv(EnvKeep); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return o, fmt.Errorf("%w: %s=%q", ErrBadSetting, EnvKeep, raw)
		}

		o.Keep = n
	}

	if raw := getenv(EnvMaxAge); raw != "" {
		d, err := parseAge(raw)
		if err != nil {
			return o, fmt.Errorf("%w: %s=%q", ErrBadSetting, EnvMaxAge, raw)
		}

		o.MaxAge = d
	}

	return o, nil
}

// parseAge reads a duration, also accepting a day suffix ("30d").
func parseAge(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, ErrBadSetting
		}

		return time.Duration(n) * hoursPerDay * time.Hour, nil
	}

	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, ErrBadSetting
	}

	return d, nil
}

// Store is the file-based usecase.LoadRunStore.
type Store struct {
	dir string
	// plansDir holds stored plans: loadplans/<projectId>/<planId>/v<N>.json.
	plansDir string
	opts     Options
	mu       sync.Mutex
}

var (
	_ usecase.LoadRunStore  = (*Store)(nil)
	_ usecase.LoadPlanStore = (*Store)(nil)
)

// New opens (and creates) DATA_DIR/loadruns. Runs that were still active when
// the previous process ended are marked failed, and retention is applied.
func New(dataDir string, opts *Options) (*Store, error) {
	s := new(Store)
	s.dir = filepath.Join(dataDir, dirName)
	s.plansDir = filepath.Join(dataDir, plansDirName)
	s.opts = *opts

	if s.opts.Now == nil {
		s.opts.Now = time.Now
	}

	err := os.MkdirAll(s.dir, dirPerm)
	if err != nil {
		return nil, fmt.Errorf("create load runs dir: %w", err)
	}

	s.recover()
	s.Prune()

	return s, nil
}

func validName(name string) bool { return nameRe.MatchString(name) && !strings.Contains(name, "..") }

func runDir(dir, id string) string { return filepath.Join(dir, id) }

// SaveRun writes run.json atomically.
func (s *Store) SaveRun(_ context.Context, run *domain.LoadRun) error {
	if !validName(run.ID) {
		return ErrBadName
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.MkdirAll(runDir(s.dir, run.ID), dirPerm)
	if err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}

	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run: %w", err)
	}

	err = writeAtomic(filepath.Join(runDir(s.dir, run.ID), runFile), data)
	if err != nil {
		return err
	}

	if run.State != "" && !run.State.Active() {
		s.pruneLocked()
	}

	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"

	err := os.WriteFile(tmp, data, filePerm)
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}

	err = os.Rename(tmp, path)
	if err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}

	return nil
}

func readRun(dir, id string) (*domain.LoadRun, error) {
	data, err := os.ReadFile(filepath.Join(runDir(dir, id), runFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("read run: %w", err)
	}

	var run domain.LoadRun

	err = json.Unmarshal(data, &run)
	if err != nil {
		return nil, fmt.Errorf("decode run %s: %w", id, err)
	}

	return &run, nil
}

// GetRun returns domain.ErrNotFound for an unknown run or one of another
// project.
func (s *Store) GetRun(_ context.Context, projectID, runID string) (*domain.LoadRun, error) {
	if !validName(runID) {
		return nil, domain.ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	run, err := readRun(s.dir, runID)
	if err != nil {
		return nil, err
	}

	if run.ProjectID != projectID {
		return nil, domain.ErrNotFound
	}

	return run, nil
}

// ListRuns returns the project's runs, newest first. Unreadable run folders
// are skipped.
func (s *Store) ListRuns(_ context.Context, projectID string) ([]domain.LoadRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []domain.LoadRun

	for _, run := range allRuns(s.dir) {
		if run.ProjectID == projectID {
			out = append(out, *run)
		}
	}

	sortNewestFirst(out)

	return out, nil
}

func sortNewestFirst(runs []domain.LoadRun) {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
}

// allRuns reads every run record; the caller holds the lock.
func allRuns(dir string) []*domain.LoadRun {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var out []*domain.LoadRun

	for _, e := range entries {
		if !e.IsDir() || !validName(e.Name()) {
			continue
		}

		run, err := readRun(dir, e.Name())
		if err == nil {
			out = append(out, run)
		}
	}

	return out
}

// DeleteRun removes a run folder with its artifacts.
func (s *Store) DeleteRun(ctx context.Context, projectID, runID string) error {
	_, err := s.GetRun(ctx, projectID, runID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = os.RemoveAll(runDir(s.dir, runID))
	if err != nil {
		return fmt.Errorf("delete run: %w", err)
	}

	return nil
}

// SaveArtifact writes a detail file of a run.
func (s *Store) SaveArtifact(ctx context.Context, projectID, runID, name string, data []byte) error {
	if !validName(name) {
		return ErrBadName
	}

	if name == runFile {
		return ErrReservedRun
	}

	_, err := s.GetRun(ctx, projectID, runID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return writeAtomic(filepath.Join(runDir(s.dir, runID), name), data)
}

// ReadArtifact returns domain.ErrNotFound for an unknown artifact.
func (s *Store) ReadArtifact(ctx context.Context, projectID, runID, name string) ([]byte, error) {
	if !validName(name) || name == runFile {
		return nil, domain.ErrNotFound
	}

	_, err := s.GetRun(ctx, projectID, runID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(runDir(s.dir, runID), name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("read artifact: %w", err)
	}

	return data, nil
}

// Prune applies the retention policy and returns how many runs it deleted.
// Active runs are never deleted.
func (s *Store) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.pruneLocked()
}

func (s *Store) pruneLocked() int {
	return prune(s.dir, &s.opts)
}

// prune deletes the runs that retention no longer allows.
func prune(dir string, opts *Options) int {
	if opts.Keep <= 0 && opts.MaxAge <= 0 {
		return 0
	}

	now := opts.Now()
	perProject := map[string][]*domain.LoadRun{}

	for _, run := range allRuns(dir) {
		if !run.State.Active() {
			perProject[run.ProjectID] = append(perProject[run.ProjectID], run)
		}
	}

	deleted := 0

	for _, runs := range perProject {
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })

		for i, run := range runs {
			if expired(opts, now, i, run) && os.RemoveAll(runDir(dir, run.ID)) == nil {
				deleted++
			}
		}
	}

	return deleted
}

// expired says whether the run, i-th newest of its project, is beyond the policy.
func expired(opts *Options, now time.Time, i int, run *domain.LoadRun) bool {
	tooMany := opts.Keep > 0 && i >= opts.Keep
	tooOld := opts.MaxAge > 0 && now.Sub(run.CreatedAt) > opts.MaxAge

	return tooMany || tooOld
}

// recover marks runs left active by a previous process as failed.
func (s *Store) recover() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, run := range allRuns(s.dir) {
		if !run.State.Active() {
			continue
		}

		ended := s.opts.Now()
		run.State = domain.LoadRunFailed
		run.Error = interrupted
		run.FinishedAt = &ended

		data, err := json.MarshalIndent(run, "", "  ")
		if err == nil {
			_ = writeAtomic(filepath.Join(runDir(s.dir, run.ID), runFile), data)
		}
	}
}
