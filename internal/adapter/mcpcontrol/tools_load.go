package mcpcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	defaultLoadRate = 10
	defaultRamp     = 5 * time.Second
	defaultHold     = 30 * time.Second
	// maxWaitSeconds bounds how long a call waits for a load run, so a tool
	// call returns before an agent's own time limit.
	maxWaitSeconds = 120
	loadActor      = "mcp"
)

var (
	errNoOperations = errors.New("generating a plan needs the contract's operations, which this server does not have: " +
		"give plan")
	errRunNeeded = errors.New("run_id is required")
)

func (s *Server) loadTools() []*tool {
	return []*tool{
		newTool("run_load_test", "Run a load test",
			"Start a load test against the project's mock. Without plan, one is generated from the contract: it "+
				"calls every GET operation, ramps to rate and holds it, and checks latency, errors and contract "+
				"violations. Give plan (YAML or JSON, target mock://<project id>) to run your own. Returns the run "+
				"at once; with wait_seconds it waits for the result. Poll with get_load_run.", kindWrite,
			object(props{
				"project": projectProp(),
				"plan":    text("A load plan in YAML or JSON. Leave out to generate one from the contract."),
				"rate":    number("Generated plan: peak iterations per second (default 10)."),
				"ramp":    text("Generated plan: time to reach the rate, for example 5s (default 5s)."),
				"hold":    text("Generated plan: how long to keep the rate, for example 30s (default 30s)."),
				"include_unsafe": flag("Generated plan: also call POST, PUT, PATCH and DELETE operations, " +
					"which change data."),
				"wait_seconds": number("Wait up to this many seconds (at most 120) for the run to end."),
			}), s.runLoadTest),
		newTool("get_load_run", "Get a load run",
			"Return a load run's state and, once it has ended, its verdict, threshold results and totals "+
				"(requests, errors, throughput, latency percentiles). With wait_seconds it waits for the run to end.",
			kindRead,
			object(props{
				"project":      projectProp(),
				"run_id":       text("The run id from run_load_test."),
				"wait_seconds": number("Wait up to this many seconds (at most 120) for the run to end."),
			}, "run_id"), s.getLoadRun),
		newTool("abort_load_run", "Abort a load run",
			"Stop a running load test. The results measured so far are kept.", kindDestroy,
			object(props{
				"project": projectProp(),
				"run_id":  text("The run id."),
			}, "run_id"), s.abortLoadRun),
	}
}

type runLoadArgs struct {
	Project       string `json:"project"`
	Plan          string `json:"plan"`
	Rate          int    `json:"rate"`
	Ramp          string `json:"ramp"`
	Hold          string `json:"hold"`
	IncludeUnsafe bool   `json:"include_unsafe"`
	WaitSeconds   int    `json:"wait_seconds"`
}

func (s *Server) runLoadTest(ctx context.Context, a *runLoadArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	plan, err := s.planFor(p.ID, a)
	if err != nil {
		return nil, err
	}

	run, err := s.deps.LoadRuns.Start(ctx, &usecase.StartLoadRun{
		ProjectID: p.ID, Plan: plan, PlanID: "", PlanVersion: 0, Role: domain.RoleEditor, Actor: loadActor,
	})
	if err != nil {
		return nil, fmt.Errorf("start the load run: %w", err)
	}

	return s.runResult(ctx, p.ID, run.ID, a.WaitSeconds)
}

// planFor returns the plan to run: the given one, or one generated from the
// project's contract.
func (s *Server) planFor(projectID string, a *runLoadArgs) (*domain.Plan, error) {
	if strings.TrimSpace(a.Plan) != "" {
		plan, err := usecase.ParsePlan([]byte(a.Plan))
		if err != nil {
			return nil, fmt.Errorf("the plan is not valid: %w", err)
		}

		return plan, nil
	}

	if s.deps.Operations == nil {
		return nil, errNoOperations
	}

	c, err := s.deps.Contracts.GetActive(projectID)
	if err != nil {
		return nil, fmt.Errorf("the project has no contract to generate a plan from: %w", err)
	}

	ops, err := s.deps.Operations.LoadOperations([]byte(c.Raw))
	if err != nil {
		return nil, fmt.Errorf("read the contract's operations: %w", err)
	}

	ramp, err := durationOr(a.Ramp, defaultRamp)
	if err != nil {
		return nil, fmt.Errorf("ramp: %w", err)
	}

	hold, err := durationOr(a.Hold, defaultHold)
	if err != nil {
		return nil, fmt.Errorf("hold: %w", err)
	}

	rate := a.Rate
	if rate <= 0 {
		rate = defaultLoadRate
	}

	plan, err := usecase.GeneratePlan(ops, &usecase.GenerateOptions{
		Name: "", ProjectID: projectID, BaseURL: "", IncludeUnsafe: a.IncludeUnsafe, Rate: rate, Ramp: ramp, Hold: hold,
	})
	if err != nil {
		return nil, fmt.Errorf("generate a plan: %w", err)
	}

	return plan, nil
}

func durationOr(v string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(v) == "" {
		return def, nil
	}

	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("use a duration such as 30s or 2m: %w", err)
	}

	return d, nil
}

type loadRunArgs struct {
	Project     string `json:"project"`
	RunID       string `json:"run_id"`
	WaitSeconds int    `json:"wait_seconds"`
}

func (s *Server) getLoadRun(ctx context.Context, a *loadRunArgs) (any, error) {
	if a.RunID == "" {
		return nil, errRunNeeded
	}

	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	return s.runResult(ctx, p.ID, a.RunID, a.WaitSeconds)
}

type abortRunArgs struct {
	Project string `json:"project"`
	RunID   string `json:"run_id"`
}

func (s *Server) abortLoadRun(ctx context.Context, a *abortRunArgs) (any, error) {
	if a.RunID == "" {
		return nil, errRunNeeded
	}

	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	err = s.deps.LoadRuns.Abort(ctx, p.ID, a.RunID)
	if err != nil {
		return nil, fmt.Errorf("abort the run: %w", err)
	}

	return s.runResult(ctx, p.ID, a.RunID, 0)
}

// runResult describes a run, after waiting up to waitSeconds for it to end.
func (s *Server) runResult(ctx context.Context, projectID, runID string, waitSeconds int) (any, error) {
	if waitSeconds > 0 {
		s.waitForRun(ctx, runID, waitSeconds)
	}

	run, err := s.deps.LoadRuns.Get(ctx, projectID, runID)
	if err != nil {
		return nil, fmt.Errorf("read the run: %w", err)
	}

	return viewRun(run), nil
}

// waitForRun blocks until the run ends, the wait is over or ctx is done.
func (s *Server) waitForRun(ctx context.Context, runID string, waitSeconds int) {
	timer := time.NewTimer(time.Duration(min(waitSeconds, maxWaitSeconds)) * time.Second)
	defer timer.Stop()

	select {
	case <-s.deps.LoadRuns.Done(runID):
	case <-timer.C:
	case <-ctx.Done():
	}
}

func viewRun(run *domain.LoadRun) map[string]any {
	out := map[string]any{
		"run_id": run.ID, "project": run.ProjectID, "state": run.State, "target": run.Target,
		"ended": run.FinishedAt != nil,
	}

	if run.Verdict != "" {
		out["verdict"] = run.Verdict
	}

	if run.AbortReason != "" {
		out["abort_reason"] = run.AbortReason
	}

	if run.Error != "" {
		out["error"] = run.Error
	}

	if len(run.Thresholds) > 0 {
		out["thresholds"] = run.Thresholds
	}

	if run.Totals != nil {
		out["totals"] = run.Totals
	}

	return out
}
