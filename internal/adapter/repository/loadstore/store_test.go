package loadstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/loadstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

var ctx = context.Background()

func run(id, project string, created time.Time, state domain.LoadRunState) *domain.LoadRun {
	r := new(domain.LoadRun)
	r.ID = id
	r.ProjectID = project
	r.CreatedAt = created
	r.State = state
	r.Plan.Name = "plan " + id

	return r
}

func open(t *testing.T, dir string, o loadstore.Options) *loadstore.Store {
	t.Helper()

	s, err := loadstore.New(dir, &o)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestStore_RoundTripAndRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir, loadstore.Options{})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i, id := range []string{"run_a", "run_b"} {
		if err := s.SaveRun(ctx, run(id, "p1", base.Add(time.Duration(i)*time.Hour), domain.LoadRunFinished)); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SaveArtifact(ctx, "p1", "run_a", "summary.json", []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}

	// A new process sees the same history.
	s2 := open(t, dir, loadstore.Options{})

	runs, _ := s2.ListRuns(ctx, "p1")
	if len(runs) != 2 || runs[0].ID != "run_b" {
		t.Fatalf("history must survive a restart, newest first: %+v", runs)
	}

	got, err := s2.ReadArtifact(ctx, "p1", "run_a", "summary.json")
	if err != nil || string(got) != `{"x":1}` {
		t.Errorf("artifact: %q %v", got, err)
	}

	if other, _ := s2.ListRuns(ctx, "p2"); len(other) != 0 {
		t.Error("another project must see nothing")
	}

	if _, err := s2.GetRun(ctx, "p2", "run_a"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another project's run: %v", err)
	}
}

func TestStore_Delete(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir, loadstore.Options{})

	_ = s.SaveRun(ctx, run("run_a", "p1", time.Now(), domain.LoadRunFinished))
	_ = s.SaveArtifact(ctx, "p1", "run_a", "samples.json", []byte("[]"))

	if err := s.DeleteRun(ctx, "p1", "run_a"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "loadruns", "run_a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder must be gone: %v", err)
	}

	if err := s.DeleteRun(ctx, "p1", "run_a"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestStore_RejectsUnsafeNames(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir(), loadstore.Options{})

	if err := s.SaveRun(ctx, run("../evil", "p1", time.Now(), domain.LoadRunFinished)); !errors.Is(err, loadstore.ErrBadName) {
		t.Errorf("traversal in id: %v", err)
	}

	_ = s.SaveRun(ctx, run("run_a", "p1", time.Now(), domain.LoadRunFinished))

	for _, name := range []string{"../x", "a/b", "", ".hidden", "a..b"} {
		if err := s.SaveArtifact(ctx, "p1", "run_a", name, nil); !errors.Is(err, loadstore.ErrBadName) {
			t.Errorf("artifact %q: %v", name, err)
		}
	}

	if err := s.SaveArtifact(ctx, "p1", "run_a", "run.json", nil); !errors.Is(err, loadstore.ErrReservedRun) {
		t.Errorf("run.json: %v", err)
	}

	if _, err := s.ReadArtifact(ctx, "p1", "run_a", "run.json"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("reading run.json as an artifact: %v", err)
	}

	if _, err := s.GetRun(ctx, "p1", "../x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get with traversal: %v", err)
	}
}

func TestStore_RetentionByCountDeletesOldest(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir(), loadstore.Options{Keep: 2})
	base := time.Now().Add(-time.Hour)

	for i, id := range []string{"r1", "r2", "r3", "r4"} {
		_ = s.SaveRun(ctx, run(id, "p1", base.Add(time.Duration(i)*time.Minute), domain.LoadRunFinished))
	}

	_ = s.SaveRun(ctx, run("o1", "p2", base, domain.LoadRunFinished))

	runs, _ := s.ListRuns(ctx, "p1")
	if len(runs) != 2 || runs[0].ID != "r4" || runs[1].ID != "r3" {
		t.Errorf("kept: %+v", runs)
	}

	if other, _ := s.ListRuns(ctx, "p2"); len(other) != 1 {
		t.Error("retention counts per project")
	}
}

func TestStore_RetentionByAgeAndActiveRunsAreSafe(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := open(t, t.TempDir(), loadstore.Options{MaxAge: 48 * time.Hour, Now: func() time.Time { return now }})

	_ = s.SaveRun(ctx, run("old", "p1", now.Add(-72*time.Hour), domain.LoadRunFinished))
	_ = s.SaveRun(ctx, run("fresh", "p1", now.Add(-time.Hour), domain.LoadRunFinished))
	_ = s.SaveRun(ctx, run("oldactive", "p1", now.Add(-100*time.Hour), domain.LoadRunRunning))

	s.Prune()

	ids := map[string]bool{}

	runs, _ := s.ListRuns(ctx, "p1")
	for _, r := range runs {
		ids[r.ID] = true
	}

	if ids["old"] || !ids["fresh"] || !ids["oldactive"] {
		t.Errorf("kept: %v", ids)
	}
}

func TestStore_RestartMarksInterruptedRunsFailed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir, loadstore.Options{})
	_ = s.SaveRun(ctx, run("run_a", "p1", time.Now(), domain.LoadRunRunning))

	s2 := open(t, dir, loadstore.Options{})

	got, err := s2.GetRun(ctx, "p1", "run_a")
	if err != nil || got.State != domain.LoadRunFailed || got.Error == "" || got.FinishedAt == nil {
		t.Errorf("interrupted run: %+v %v", got, err)
	}
}

func TestOptionsFromEnv(t *testing.T) {
	t.Parallel()

	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	o, err := loadstore.OptionsFromEnv(env(nil))
	if err != nil || o.Keep != loadstore.DefaultKeep || o.MaxAge != loadstore.DefaultMaxAge {
		t.Errorf("defaults: %+v %v", o, err)
	}

	o, err = loadstore.OptionsFromEnv(env(map[string]string{"LOAD_RUNS_KEEP": "7", "LOAD_RUNS_MAX_AGE": "10d"}))
	if err != nil || o.Keep != 7 || o.MaxAge != 240*time.Hour {
		t.Errorf("custom: %+v %v", o, err)
	}

	o, _ = loadstore.OptionsFromEnv(env(map[string]string{"LOAD_RUNS_KEEP": "0", "LOAD_RUNS_MAX_AGE": "0"}))
	if o.Keep != 0 || o.MaxAge != 0 {
		t.Errorf("off: %+v", o)
	}

	for _, bad := range []map[string]string{{"LOAD_RUNS_KEEP": "x"}, {"LOAD_RUNS_KEEP": "-1"}, {"LOAD_RUNS_MAX_AGE": "soon"}} {
		if _, err := loadstore.OptionsFromEnv(env(bad)); !errors.Is(err, loadstore.ErrBadSetting) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}
