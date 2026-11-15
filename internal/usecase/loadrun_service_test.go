package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type memRunStore struct {
	mu        sync.Mutex
	runs      map[string]domain.LoadRun
	artifacts map[string][]byte
}

func newMemRunStore() *memRunStore {
	return &memRunStore{runs: map[string]domain.LoadRun{}, artifacts: map[string][]byte{}}
}

func (m *memRunStore) SaveRun(_ context.Context, r *domain.LoadRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.runs[r.ProjectID+"/"+r.ID] = *r

	return nil
}

func (m *memRunStore) GetRun(_ context.Context, p, id string) (*domain.LoadRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.runs[p+"/"+id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return &r, nil
}

func (m *memRunStore) ListRuns(_ context.Context, p string) ([]domain.LoadRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []domain.LoadRun

	for _, r := range m.runs {
		if r.ProjectID == p {
			out = append(out, r)
		}
	}

	return out, nil
}

func (m *memRunStore) DeleteRun(_ context.Context, p, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.runs[p+"/"+id]; !ok {
		return domain.ErrNotFound
	}

	delete(m.runs, p+"/"+id)

	return nil
}

func (m *memRunStore) SaveArtifact(_ context.Context, p, id, name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.artifacts[p+"/"+id+"/"+name] = data

	return nil
}

func (m *memRunStore) ReadArtifact(_ context.Context, p, id, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	d, ok := m.artifacts[p+"/"+id+"/"+name]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return d, nil
}

// fakeEngine runs until released or cancelled.
type fakeEngine struct {
	outcome *usecase.LoadOutcome
	err     error
	// hold makes Execute wait for release or ctx.Done.
	hold    bool
	release chan struct{}
	started chan struct{}
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{release: make(chan struct{}), started: make(chan struct{}, 8)}
}

func (f *fakeEngine) Execute(ctx context.Context, in *usecase.LoadExecution) (*usecase.LoadOutcome, error) {
	in.Progress(usecase.LoadProgress{Requests: 42})

	f.started <- struct{}{}

	if f.hold {
		select {
		case <-ctx.Done():
		case <-f.release:
		}
	}

	return f.outcome, f.err
}

func passingOutcome() *usecase.LoadOutcome {
	o := new(usecase.LoadOutcome)
	o.Totals = &domain.LoadTotals{Requests: 100}
	o.Thresholds = []domain.LoadThresholdResult{{Passed: true, HasData: true}}
	o.Artifacts = map[string][]byte{"summary.json": []byte(`{}`)}

	return o
}

func runnablePlan(base string) *domain.Plan {
	p := planFor(base, "api.example.com")
	p.Scenarios = []domain.Scenario{{Name: "s", Weight: 1, Steps: []domain.Step{{Method: "GET", Path: "/x"}}}}

	return p
}

func newRunService(t *testing.T, eng *fakeEngine, mutate func(*usecase.LoadRunDeps)) (*usecase.LoadRunService, *memRunStore) {
	t.Helper()

	store := newMemRunStore()
	deps := &usecase.LoadRunDeps{Engine: eng, Store: store, MockBase: "http://127.0.0.1:8080"}

	if mutate != nil {
		mutate(deps)
	}

	svc, err := usecase.NewLoadRunService(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}

	return svc, store
}

func start(t *testing.T, svc *usecase.LoadRunService, project string, plan *domain.Plan) (*domain.LoadRun, error) {
	t.Helper()

	return svc.Start(context.Background(), &usecase.StartLoadRun{
		ProjectID: project, Plan: plan, Role: domain.RoleAdmin, Actor: "ann",
	})
}

func waitDone(t *testing.T, svc *usecase.LoadRunService, id string) {
	t.Helper()

	select {
	case <-svc.Done(id):
	case <-time.After(3 * time.Second):
		t.Fatal("run did not end")
	}
}

func TestLoadRun_FinishesWithVerdict(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.outcome = passingOutcome()
	svc, store := newRunService(t, eng, nil)

	run, err := start(t, svc, "p1", runnablePlan("mock://p1"))
	if err != nil {
		t.Fatal(err)
	}

	if run.State != domain.LoadRunPending || run.Target != "http://127.0.0.1:8080/mock/p1" || run.Actor != "ann" {
		t.Errorf("accepted run: %+v", run)
	}

	waitDone(t, svc, run.ID)

	got, err := svc.Get(context.Background(), "p1", run.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.State != domain.LoadRunFinished || got.Verdict != domain.VerdictPass || got.StartedAt == nil ||
		got.FinishedAt == nil || got.Totals == nil || got.Totals.Requests != 100 || got.Plan.Scenarios[0].Name != "s" {
		t.Errorf("finished run: %+v", got)
	}

	if _, err := svc.Artifact(context.Background(), "p1", run.ID, "summary.json"); err != nil {
		t.Errorf("artifact: %v", err)
	}

	if len(store.runs) != 1 {
		t.Errorf("runs stored: %d", len(store.runs))
	}
}

func TestLoadRun_FailedThresholdIsFailVerdict(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.outcome = passingOutcome()
	eng.outcome.Thresholds = append(eng.outcome.Thresholds, domain.LoadThresholdResult{HasData: true})
	svc, _ := newRunService(t, eng, nil)

	run, _ := start(t, svc, "p1", runnablePlan("mock://p1"))
	waitDone(t, svc, run.ID)

	got, _ := svc.Get(context.Background(), "p1", run.ID)
	if got.State != domain.LoadRunFinished || got.Verdict != domain.VerdictFail {
		t.Errorf("run: %+v", got)
	}
}

func TestLoadRun_AbortKeepsPartialResults(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.hold = true
	eng.outcome = passingOutcome()
	svc, _ := newRunService(t, eng, nil)

	run, _ := start(t, svc, "p1", runnablePlan("mock://p1"))
	<-eng.started

	if p, ok := svc.Progress(run.ID); !ok || p.Requests != 42 {
		t.Errorf("progress: %+v %v", p, ok)
	}

	if err := svc.Abort(context.Background(), "p1", run.ID); err != nil {
		t.Fatal(err)
	}

	waitDone(t, svc, run.ID)

	got, _ := svc.Get(context.Background(), "p1", run.ID)
	if got.State != domain.LoadRunAborted || got.Verdict != domain.VerdictFail || got.AbortReason == "" || got.Totals == nil {
		t.Errorf("aborted run: %+v", got)
	}

	if err := svc.Abort(context.Background(), "p1", run.ID); !errors.Is(err, domain.ErrLoadRunNotLive) {
		t.Errorf("abort of an ended run: %v", err)
	}

	if err := svc.Abort(context.Background(), "p1", "nope"); !errors.Is(err, domain.ErrLoadRunNotFound) {
		t.Errorf("abort of an unknown run: %v", err)
	}
}

func TestLoadRun_ThresholdAbortAndEngineFailure(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.outcome = passingOutcome()
	eng.outcome.AbortReason = "errors.rate < 0.01 failed"
	svc, _ := newRunService(t, eng, nil)

	run, _ := start(t, svc, "p1", runnablePlan("mock://p1"))
	waitDone(t, svc, run.ID)

	got, _ := svc.Get(context.Background(), "p1", run.ID)
	if got.State != domain.LoadRunAborted || got.AbortReason != "errors.rate < 0.01 failed" || got.Verdict != domain.VerdictFail {
		t.Errorf("threshold abort: %+v", got)
	}

	bad := newFakeEngine()
	bad.err = errors.New("boom")
	svc2, _ := newRunService(t, bad, nil)

	run2, _ := start(t, svc2, "p1", runnablePlan("mock://p1"))
	waitDone(t, svc2, run2.ID)

	got2, _ := svc2.Get(context.Background(), "p1", run2.ID)
	if got2.State != domain.LoadRunFailed || got2.Error != "boom" || got2.Verdict != "" {
		t.Errorf("failed run: %+v", got2)
	}
}

func TestLoadRun_OneActiveRunPerProject(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.hold = true
	eng.outcome = passingOutcome()
	svc, _ := newRunService(t, eng, nil)

	first, err := start(t, svc, "p1", runnablePlan("mock://p1"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := start(t, svc, "p1", runnablePlan("mock://p1")); !errors.Is(err, domain.ErrLoadRunActive) {
		t.Errorf("second run of the project: %v", err)
	}

	other, err := start(t, svc, "p2", runnablePlan("mock://p2"))
	if err != nil {
		t.Errorf("another project must be free: %v", err)
	}

	if err := svc.Delete(context.Background(), "p1", first.ID); !errors.Is(err, domain.ErrLoadRunActive) {
		t.Errorf("delete of an active run: %v", err)
	}

	close(eng.release)
	waitDone(t, svc, first.ID)
	waitDone(t, svc, other.ID)

	if err := svc.Delete(context.Background(), "p1", first.ID); err != nil {
		t.Errorf("delete of an ended run: %v", err)
	}

	if _, err := svc.Get(context.Background(), "p1", first.ID); !errors.Is(err, domain.ErrLoadRunNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestLoadRun_ConfigurableConcurrency(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.hold = true
	eng.outcome = passingOutcome()
	svc, _ := newRunService(t, eng, func(d *usecase.LoadRunDeps) { d.MaxActivePerProject = 2 })

	for range 2 {
		if _, err := start(t, svc, "p1", runnablePlan("mock://p1")); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := start(t, svc, "p1", runnablePlan("mock://p1")); !errors.Is(err, domain.ErrLoadRunActive) {
		t.Errorf("third run: %v", err)
	}

	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRun_RefusesBadRequests(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	svc, store := newRunService(t, eng, nil)

	if _, err := start(t, svc, "p1", nil); !errors.Is(err, usecase.ErrLoadRunNoPlan) {
		t.Errorf("no plan: %v", err)
	}

	if _, err := start(t, svc, "p1", planFor("mock://p1")); !errors.Is(err, domain.ErrLoadNoScenarios) {
		t.Errorf("invalid plan: %v", err)
	}

	if _, err := start(t, svc, "p1", runnablePlan("mock://p2")); !errors.Is(err, usecase.ErrLoadRunProject) {
		t.Errorf("another project's mock: %v", err)
	}

	_, err := svc.Start(context.Background(), &usecase.StartLoadRun{
		ProjectID: "p1", Plan: runnablePlan("https://api.example.com"), Role: domain.RoleViewer,
	})
	if !errors.Is(err, usecase.ErrLoadTargetRole) {
		t.Errorf("viewer against an external target: %v", err)
	}

	denied := runnablePlan("https://evil.example.org")
	if _, err := start(t, svc, "p1", denied); !errors.Is(err, usecase.ErrLoadTargetHost) {
		t.Errorf("host outside allowHosts: %v", err)
	}

	if len(store.runs) != 0 {
		t.Error("a refused request must leave no record")
	}
}

func TestLoadRun_ShutdownStopsRuns(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.hold = true
	eng.outcome = passingOutcome()
	svc, _ := newRunService(t, eng, nil)

	run, _ := start(t, svc, "p1", runnablePlan("mock://p1"))
	<-eng.started

	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, _ := svc.Get(context.Background(), "p1", run.ID)
	if got.State != domain.LoadRunAborted || got.AbortReason == "" {
		t.Errorf("run after shutdown: %+v", got)
	}
}

func TestNewLoadRunService_NeedsCollaborators(t *testing.T) {
	t.Parallel()

	if _, err := usecase.NewLoadRunService(context.Background(), new(usecase.LoadRunDeps)); !errors.Is(err, usecase.ErrLoadRunNotReady) {
		t.Errorf("err = %v", err)
	}
}

// artifactFailStore cannot store run artifacts.
type artifactFailStore struct{ *memRunStore }

func (artifactFailStore) SaveArtifact(context.Context, string, string, string, []byte) error {
	return errors.New("disk full")
}

func TestLoadRun_LogsStoreFailures(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		logs []string
	)

	eng := newFakeEngine()
	eng.outcome = passingOutcome()

	svc, err := usecase.NewLoadRunService(context.Background(), &usecase.LoadRunDeps{
		Engine: eng, Store: artifactFailStore{newMemRunStore()}, MockBase: "http://127.0.0.1:8080",
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()

			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	run, err := start(t, svc, "p1", runnablePlan("mock://p1"))
	if err != nil {
		t.Fatal(err)
	}

	waitDone(t, svc, run.ID)

	mu.Lock()
	defer mu.Unlock()

	if len(logs) != 1 || !strings.Contains(logs[0], "summary.json") || !strings.Contains(logs[0], "disk full") {
		t.Errorf("logs = %q, want one line about summary.json", logs)
	}
}

// gatedStore pauses the run's goroutine right after the record of a finished
// run was stored, the moment a polling client sees the run as ended.
type gatedStore struct {
	*memRunStore

	saved   chan struct{}
	proceed chan struct{}
}

func (g gatedStore) SaveRun(ctx context.Context, r *domain.LoadRun) error {
	err := g.memRunStore.SaveRun(ctx, r)

	if r.State == domain.LoadRunFinished {
		g.saved <- struct{}{}

		<-g.proceed
	}

	return err
}

// A client that polls a run until it has ended must be able to start the next
// run, or delete the finished one, at once.
func TestLoadRun_SlotIsFreeOnceTheRunShowsAsEnded(t *testing.T) {
	t.Parallel()

	eng := newFakeEngine()
	eng.outcome = passingOutcome()

	store := gatedStore{newMemRunStore(), make(chan struct{}, 4), make(chan struct{})}

	svc, err := usecase.NewLoadRunService(context.Background(), &usecase.LoadRunDeps{
		Engine: eng, Store: store, MockBase: "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := start(t, svc, "p1", runnablePlan("mock://p1"))
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-store.saved:
	case <-time.After(3 * time.Second):
		t.Fatal("the run did not end")
	}

	// The record says "finished"; the run's goroutine has not returned yet.
	second, err := start(t, svc, "p1", runnablePlan("mock://p1"))
	if err != nil {
		t.Fatalf("starting the next run after the first one ended: %v", err)
	}

	deleted := make(chan error, 1)

	go func() { deleted <- svc.Delete(context.Background(), "p1", first.ID) }()

	close(store.proceed)

	if err = <-deleted; err != nil {
		t.Errorf("deleting the finished run: %v", err)
	}

	waitDone(t, svc, second.ID)
}
