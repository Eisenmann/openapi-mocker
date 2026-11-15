package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	defaultMaxActiveRuns = 1
	runIDBytes           = 8
	runIDPrefix          = "run_"
	userAbortReason      = "aborted by the user"
	shutdownReason       = "the server is shutting down"
)

// Errors of the run service beyond those of the domain.
var (
	ErrLoadRunNoPlan   = errors.New("a load run needs a plan")
	ErrLoadRunProject  = errors.New("a mock:// target must name the project the run belongs to")
	ErrLoadRunNotReady = errors.New("the load run service has no engine or store")
)

// LoadRunDeps are the collaborators of the run service.
type LoadRunDeps struct {
	Engine LoadEngine
	Store  LoadRunStore
	Caps   LoadCaps
	// MockBase is the server's own address, the base of mock:// targets.
	MockBase string
	// MaxActivePerProject limits concurrent runs of a project (default 1).
	MaxActivePerProject int
	// Now and NewID may be replaced in tests.
	Now   func() time.Time
	NewID func() string
	// Logf reports what could not be stored (default log.Printf).
	Logf func(format string, args ...any)
}

// StartLoadRun is a request to start a run.
type StartLoadRun struct {
	ProjectID string
	Plan      *domain.Plan
	// PlanID and PlanVersion identify a stored plan; empty for an inline one.
	PlanID      string
	PlanVersion int
	Role        domain.Role
	Actor       string
}

// liveRun is a run that has not ended.
type liveRun struct {
	projectID string
	ctx       context.Context //nolint:containedctx // the run's own lifetime
	cancel    context.CancelFunc
	done      chan struct{}
	progress  atomic.Pointer[LoadProgress]
	// reason is why the run was stopped from outside, empty otherwise.
	reason atomic.Pointer[string]
	// ended is set once the outcome is known, before the final record is stored:
	// from then on the run no longer holds its project's slot, so a client that
	// sees the run as ended can start the next one at once.
	ended atomic.Bool
}

// LoadRunService starts, tracks and stops load runs.
type LoadRunService struct {
	deps LoadRunDeps
	base context.Context //nolint:containedctx // the server's lifetime, the parent of every run

	mu   sync.Mutex
	live map[string]*liveRun
}

// NewLoadRunService returns a service whose runs live as long as base.
func NewLoadRunService(base context.Context, deps *LoadRunDeps) (*LoadRunService, error) {
	if deps.Engine == nil || deps.Store == nil {
		return nil, ErrLoadRunNotReady
	}

	s := new(LoadRunService)
	s.deps = *deps
	s.base = base
	s.live = make(map[string]*liveRun)

	if s.deps.MaxActivePerProject <= 0 {
		s.deps.MaxActivePerProject = defaultMaxActiveRuns
	}

	if s.deps.Now == nil {
		s.deps.Now = time.Now
	}

	if s.deps.NewID == nil {
		s.deps.NewID = newRunID
	}

	if s.deps.Logf == nil {
		s.deps.Logf = log.Printf
	}

	return s, nil
}

func newRunID() string {
	b := make([]byte, runIDBytes)
	_, _ = rand.Read(b)

	return runIDPrefix + hex.EncodeToString(b)
}

// Start validates the plan, resolves its target and begins the run in the
// background. The returned record is the run as it was accepted.
func (s *LoadRunService) Start(ctx context.Context, req *StartLoadRun) (*domain.LoadRun, error) {
	target, err := s.prepare(req)
	if err != nil {
		return nil, err
	}

	run := new(domain.LoadRun)
	run.ID = s.deps.NewID()
	run.ProjectID = req.ProjectID
	run.PlanID = req.PlanID
	run.PlanVersion = req.PlanVersion
	run.Plan = *req.Plan
	run.Target = target.BaseURL.String()
	run.Actor = req.Actor
	run.State = domain.LoadRunPending
	run.CreatedAt = s.deps.Now()

	lr, err := s.register(run)
	if err != nil {
		return nil, err
	}

	err = s.deps.Store.SaveRun(ctx, run)
	if err != nil {
		s.release(run.ID, lr)

		return nil, fmt.Errorf("save load run: %w", err)
	}

	snapshot := *run

	go s.execute(lr.ctx, lr, run, target) //nolint:contextcheck // the run outlives the request that started it

	return &snapshot, nil
}

// prepare checks the request and resolves the target.
func (s *LoadRunService) prepare(req *StartLoadRun) (*LoadTarget, error) {
	if req.Plan == nil {
		return nil, ErrLoadRunNoPlan
	}

	err := req.Plan.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid load plan: %w", err)
	}

	target, err := ResolveLoadTarget(req.Plan, req.Role, s.deps.Caps, s.deps.MockBase)
	if err != nil {
		return nil, err
	}

	if target.Mock && target.ProjectID != req.ProjectID {
		return nil, ErrLoadRunProject
	}

	return target, nil
}

// register claims a slot for the run, or fails when the project is busy.
func (s *LoadRunService) register(run *domain.LoadRun) (*liveRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	active := 0

	for _, l := range s.live {
		if l.projectID == run.ProjectID && !l.ended.Load() {
			active++
		}
	}

	if active >= s.deps.MaxActivePerProject {
		return nil, domain.ErrLoadRunActive
	}

	lr := new(liveRun)
	lr.projectID = run.ProjectID
	lr.done = make(chan struct{})
	lr.ctx, lr.cancel = context.WithCancel(s.base)
	s.live[run.ID] = lr

	return lr, nil
}

func (s *LoadRunService) release(runID string, lr *liveRun) {
	s.mu.Lock()
	delete(s.live, runID)
	s.mu.Unlock()

	lr.cancel()
	close(lr.done)
}

// execute runs the plan and records how it ended.
func (s *LoadRunService) execute(ctx context.Context, lr *liveRun, run *domain.LoadRun, target *LoadTarget) {
	defer s.release(run.ID, lr)

	// Saving must work after the run was cancelled.
	saveCtx := context.WithoutCancel(ctx)

	started := s.deps.Now()
	run.State = domain.LoadRunRunning
	run.StartedAt = &started
	s.save(saveCtx, run)

	in := new(LoadExecution)
	in.RunID = run.ID
	in.ProjectID = run.ProjectID
	in.Actor = run.Actor
	in.Plan = &run.Plan
	in.Target = target
	in.Progress = func(p LoadProgress) { lr.progress.Store(&p) }

	outcome, err := s.deps.Engine.Execute(ctx, in)

	s.finish(saveCtx, lr, run, outcome, err)
}

// finish decides the final state and stores the record and the artifacts.
func (s *LoadRunService) finish(
	ctx context.Context, lr *liveRun, run *domain.LoadRun, outcome *LoadOutcome, execErr error,
) {
	ended := s.deps.Now()
	run.FinishedAt = &ended

	if outcome != nil {
		run.Thresholds = outcome.Thresholds
		run.Totals = outcome.Totals
	}

	switch reason := lr.reason.Load(); {
	case execErr != nil:
		run.State = domain.LoadRunFailed
		run.Error = execErr.Error()
	case reason != nil:
		run.State = domain.LoadRunAborted
		run.AbortReason = *reason
	case outcome != nil && outcome.AbortReason != "":
		run.State = domain.LoadRunAborted
		run.AbortReason = outcome.AbortReason
	default:
		run.State = domain.LoadRunFinished
	}

	if run.State != domain.LoadRunFailed {
		run.Verdict = verdictOf(run)
	}

	lr.ended.Store(true)

	if outcome != nil {
		s.saveArtifacts(ctx, run, outcome.Artifacts)
	}

	s.save(ctx, run)
}

// saveArtifacts stores the detail files of a run; failures are logged.
func (s *LoadRunService) saveArtifacts(ctx context.Context, run *domain.LoadRun, artifacts map[string][]byte) {
	for name, data := range artifacts {
		err := s.deps.Store.SaveArtifact(ctx, run.ProjectID, run.ID, name, data)
		if err != nil {
			s.deps.Logf("load run %s: saving %s failed: %v", run.ID, name, err)
		}
	}
}

// save stores the run record; a failure cannot fail the run, so it is logged.
func (s *LoadRunService) save(ctx context.Context, run *domain.LoadRun) {
	err := s.deps.Store.SaveRun(ctx, run)
	if err != nil {
		s.deps.Logf("load run %s: saving the record failed: %v", run.ID, err)
	}
}

// verdictOf is pass only when the run ran to its end and every threshold
// held; a run stopped from outside never passes.
func verdictOf(run *domain.LoadRun) domain.LoadVerdict {
	if run.State != domain.LoadRunFinished {
		return domain.VerdictFail
	}

	for i := range run.Thresholds {
		if !run.Thresholds[i].Passed {
			return domain.VerdictFail
		}
	}

	return domain.VerdictPass
}

// Abort stops an active run; its partial results are kept.
func (s *LoadRunService) Abort(ctx context.Context, projectID, runID string) error {
	s.mu.Lock()
	lr, ok := s.live[runID]
	s.mu.Unlock()

	if !ok || lr.projectID != projectID || lr.ended.Load() {
		_, err := s.deps.Store.GetRun(ctx, projectID, runID)
		if err != nil {
			return domain.ErrLoadRunNotFound
		}

		return domain.ErrLoadRunNotLive
	}

	stopLive(lr, userAbortReason)

	return nil
}

func stopLive(lr *liveRun, reason string) {
	lr.reason.CompareAndSwap(nil, &reason)

	lr.cancel()
}

// Get returns a run's record.
func (s *LoadRunService) Get(ctx context.Context, projectID, runID string) (*domain.LoadRun, error) {
	run, err := s.deps.Store.GetRun(ctx, projectID, runID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrLoadRunNotFound
		}

		return nil, fmt.Errorf("get load run: %w", err)
	}

	return run, nil
}

// List returns the project's runs, newest first.
func (s *LoadRunService) List(ctx context.Context, projectID string) ([]domain.LoadRun, error) {
	runs, err := s.deps.Store.ListRuns(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list load runs: %w", err)
	}

	return runs, nil
}

// Progress is the latest live reading of an active run.
func (s *LoadRunService) Progress(runID string) (LoadProgress, bool) {
	s.mu.Lock()
	lr, ok := s.live[runID]
	s.mu.Unlock()

	if !ok {
		return LoadProgress{}, false
	}

	p := lr.progress.Load()
	if p == nil {
		return LoadProgress{}, false
	}

	return *p, true
}

// Artifact returns a detail file of a run (a report source).
func (s *LoadRunService) Artifact(ctx context.Context, projectID, runID, name string) ([]byte, error) {
	data, err := s.deps.Store.ReadArtifact(ctx, projectID, runID, name)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("read load artifact: %w", err)
	}

	return data, nil
}

// Delete removes a finished run; an active one must be aborted first.
func (s *LoadRunService) Delete(ctx context.Context, projectID, runID string) error {
	s.mu.Lock()
	lr, active := s.live[runID]
	s.mu.Unlock()

	if active {
		if !lr.ended.Load() {
			return domain.ErrLoadRunActive
		}

		// The run is over and is storing its record; wait for that so the record
		// is not written again after it was deleted.
		select {
		case <-lr.done:
		case <-ctx.Done():
			return fmt.Errorf("waiting for the run to be stored: %w", ctx.Err())
		}
	}

	err := s.deps.Store.DeleteRun(ctx, projectID, runID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrLoadRunNotFound
		}

		return fmt.Errorf("delete load run: %w", err)
	}

	return nil
}

// Done returns a channel closed when the run has ended (closed at once for
// a run that is not active).
func (s *LoadRunService) Done(runID string) <-chan struct{} {
	s.mu.Lock()
	lr, ok := s.live[runID]
	s.mu.Unlock()

	if !ok {
		ch := make(chan struct{})
		close(ch)

		return ch
	}

	return lr.done
}

// Shutdown stops every active run and waits for them, or for ctx.
func (s *LoadRunService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	runs := make([]*liveRun, 0, len(s.live))

	for _, lr := range s.live {
		runs = append(runs, lr)
	}

	s.mu.Unlock()

	for _, lr := range runs {
		stopLive(lr, shutdownReason)
	}

	for _, lr := range runs {
		select {
		case <-lr.done:
		case <-ctx.Done():
			return fmt.Errorf("waiting for load runs: %w", ctx.Err())
		}
	}

	return nil
}
