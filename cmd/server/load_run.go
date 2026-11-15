package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/engine"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const loadRunUsage = `Usage: openapi-mocker load run PLAN [options]

Runs a load plan and writes a report. The plan is a YAML or JSON file; data
files it names are read relative to the plan's directory, and secrets
(target.auth.ref) come from environment variables.

  --target URL      load this base URL instead of the plan's target (its host is allowed)
  --server URL      where this server runs, for mock://PROJECT targets (default http://127.0.0.1:$PORT)
  --contract FILE   the contract for operation steps and contract checks
  --data-dir DIR    read the contract of a mock:// project from this data directory
                    (default $DATA_DIR or ./data)
  --out DIR         report directory (default load-report)
  --format LIST     report files: html, json, junit, csv (default: the plan's outputs, else html,json)
  --quiet           no progress lines

The command applies no server limits: you are running load from your own machine.

Exit status: 0 all thresholds passed, 1 a threshold failed or the run was
aborted, 2 usage error or an invalid plan.
`

type runOptions struct {
	target, server, contract, dataDir, out, format string
	quiet                                          bool
}

func runLoadRun(args []string, stdout, stderr io.Writer) int {
	opts := new(runOptions)

	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.StringVar(&opts.target, "target", "", "")
	flags.StringVar(&opts.server, "server", "", "")
	flags.StringVar(&opts.contract, "contract", "", "")
	flags.StringVar(&opts.dataDir, "data-dir", getEnv("DATA_DIR", "./data"), "")
	flags.StringVar(&opts.out, "out", "load-report", "")
	flags.StringVar(&opts.format, "format", "", "")
	flags.BoolVar(&opts.quiet, "quiet", false, "")

	files, err := parseInterspersed(flags, args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, loadRunUsage)

		return exitOK
	}

	if err == nil && len(files) != 1 {
		err = fmt.Errorf("%w: want one plan file", errLoadUsage)
	}

	if err != nil {
		return loadFail(stderr, loadRunUsage, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return execLoadRun(ctx, files[0], opts, stdout, stderr)
}

// execLoadRun prepares and runs the plan and writes the report.
func execLoadRun(ctx context.Context, planPath string, opts *runOptions, stdout, stderr io.Writer) int {
	plan, target, formats, err := prepareLoadRun(planPath, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return loadtest.ExitPlanError
	}

	in := new(usecase.LoadExecution)
	in.RunID = fmt.Sprintf("cli-%d", time.Now().Unix())
	in.ProjectID = target.ProjectID
	in.Actor = "cli"
	in.Plan = plan
	in.Target = target

	if !opts.quiet {
		in.Progress = progressPrinter(stderr)
	}

	fmt.Fprintf(stderr, "running %s against %s\n", describePlan(plan), target.BaseURL)

	out, err := newCLIEngine(planPath, opts).Execute(ctx, in)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return loadtest.ExitPlanError
	}

	err = writeLoadReport(opts.out, formats, out)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return loadtest.ExitPlanError
	}

	printLoadSummary(stdout, out, opts.out, ctx.Err() != nil)

	return loadExitCode(out, ctx.Err() != nil)
}

func prepareLoadRun(
	planPath string, opts *runOptions,
) (*domain.Plan, *usecase.LoadTarget, []domain.OutputFormat, error) {
	plan, err := readPlanFile(planPath)
	if err != nil {
		return nil, nil, nil, err
	}

	err = overrideTarget(plan, opts.target)
	if err != nil {
		return nil, nil, nil, err
	}

	server := opts.server
	if server == "" {
		server = "http://127.0.0.1:" + getEnv("PORT", "8080")
	}

	// A person running the command on their own machine is the admin of it, and
	// the server's limits belong to a shared server, not to this command.
	target, err := usecase.ResolveLoadTarget(plan, domain.RoleAdmin, usecase.LoadCaps{}, server)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", planPath, err)
	}

	formats, err := reportFormats(opts.format, plan)
	if err != nil {
		return nil, nil, nil, err
	}

	return plan, target, formats, nil
}

// overrideTarget points the plan at another base URL and allows its host: the
// person typed it on the command line, which is the allowance.
func overrideTarget(plan *domain.Plan, target string) error {
	if target == "" {
		return nil
	}

	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: --target %q is not a URL", errLoadUsage, target)
	}

	plan.Target.BaseURL = target
	if !slices.Contains(plan.Target.AllowHosts, u.Hostname()) {
		plan.Target.AllowHosts = append(plan.Target.AllowHosts, u.Hostname())
	}

	return nil
}

// reportFormats reads --format, falling back to the plan's outputs, then to
// html and json.
func reportFormats(flagValue string, plan *domain.Plan) ([]domain.OutputFormat, error) {
	var names []string

	switch {
	case flagValue != "":
		names = strings.Split(flagValue, ",")
	case len(plan.Outputs) > 0:
		for _, o := range plan.Outputs {
			names = append(names, string(o.Format))
		}
	default:
		names = []string{"html", formatJSON}
	}

	var out []domain.OutputFormat

	for _, n := range names {
		f := domain.OutputFormat(strings.TrimSpace(n))
		if !slices.Contains([]domain.OutputFormat{
			domain.OutputHTML, domain.OutputJSON, domain.OutputJUnit, domain.OutputCSV,
		}, f) {
			return nil, fmt.Errorf("%w: unknown report format %q (use html, json, junit or csv)", errLoadUsage, n)
		}

		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}

	return out, nil
}

// newCLIEngine wires the engine for a command-line run: data files next to the
// plan, secrets from the environment, the contract from a file or the store.
func newCLIEngine(planPath string, opts *runOptions) *engine.Engine {
	oa := openapi.NewEngine()

	cfg := new(engine.Config)
	cfg.Operations = oa
	cfg.Data = os.DirFS(planDir(planPath))
	cfg.Secrets = os.Getenv
	cfg.Version = version
	cfg.NewValidator = func(raw []byte, every int) (usecase.ResponseValidator, error) {
		return oa.NewResponseValidator(raw, every) //nolint:wrapcheck // the engine wraps it.
	}
	cfg.Contract = func(ctx context.Context, projectID string) ([]byte, error) {
		if opts.contract != "" {
			return os.ReadFile(opts.contract)
		}

		if projectID == "" {
			return nil, errNoContractSource
		}

		return storedContract(ctx, opts.dataDir, projectID)
	}

	return engine.New(cfg)
}

// storedContract returns the raw active contract of a project in the data dir.
func storedContract(ctx context.Context, dataDir, projectID string) ([]byte, error) {
	if _, err := os.Stat(dataDir); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", errNoDataDir, dataDir)
	}

	store, err := jsonstore.New(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open data directory: %w", err)
	}

	_ = ctx

	c, err := store.GetActive(projectID)
	if err != nil {
		return nil, fmt.Errorf("project %s has no active contract: %w", projectID, err)
	}

	return []byte(c.Raw), nil
}

// progressPrinter prints one line every five seconds, so a CI log stays short.
func progressPrinter(w io.Writer) func(usecase.LoadProgress) {
	return func(p usecase.LoadProgress) {
		secs := int(p.Elapsed.Seconds())
		if secs == 0 || secs%progressEvery != 0 {
			return
		}

		fmt.Fprintf(w, "[%4ds] %d requests  %.1f rps  p95 %.1fms  errors %d  dropped %d  in-flight %d\n",
			secs, p.Requests, p.RPS, p.P95Ms, p.Errors, p.Dropped, p.InFlight)
	}
}

const progressEvery = 5

var (
	errNoContractSource = errors.New("this plan needs a contract; pass --contract FILE")
	errNoDataDir        = errors.New("data directory does not exist (use --data-dir or --contract)")
)

// loadExitCode is 1 when the run was aborted or interrupted or a threshold failed.
func loadExitCode(out *usecase.LoadOutcome, interrupted bool) int {
	if interrupted || out.AbortReason != "" {
		return loadtest.ExitThresholdFailed
	}

	for i := range out.Thresholds {
		if !out.Thresholds[i].Passed {
			return loadtest.ExitThresholdFailed
		}
	}

	return loadtest.ExitPass
}

func printLoadSummary(w io.Writer, out *usecase.LoadOutcome, dir string, interrupted bool) {
	t := out.Totals

	fmt.Fprintf(w, "\n%d requests, %d errors (%.2f%%), %.1f rps, p50 %.1fms, p95 %.1fms, p99 %.1fms\n",
		t.Requests, t.Errors, t.ErrorRate*percentOf, t.ThroughputRPS, t.LatencyP50Ms, t.LatencyP95Ms, t.LatencyP99Ms)

	if t.Dropped > 0 || t.ContractViolations > 0 {
		fmt.Fprintf(w, "%d dropped arrivals, %d contract violations\n", t.Dropped, t.ContractViolations)
	}

	if t.Untrustworthy {
		fmt.Fprintf(w, "WARNING: the load generator could not keep up; figures may be too optimistic (%s)\n",
			t.UntrustworthyReason)
	}

	for i := range out.Thresholds {
		r := &out.Thresholds[i]

		mark := "PASS"
		if !r.Passed {
			mark = "FAIL"
		}

		fmt.Fprintf(w, "  %s  %s %s %g  (observed %s)\n", mark, thresholdName(&r.Threshold), r.Threshold.Op,
			r.Threshold.Value, observedText(r))
	}

	switch {
	case out.AbortReason != "":
		fmt.Fprintf(w, "ABORTED: %s\n", out.AbortReason)
	case interrupted:
		fmt.Fprintln(w, "INTERRUPTED: the report covers the load sent so far")
	}

	fmt.Fprintf(w, "report: %s\n", dir)
}

const percentOf = 100

func thresholdName(t *domain.Threshold) string {
	if t.Scope == "" {
		return t.Metric
	}

	return fmt.Sprintf("%s{%s}", t.Metric, t.Scope)
}

func observedText(r *domain.LoadThresholdResult) string {
	if !r.HasData {
		return "no data"
	}

	return fmt.Sprintf("%g", r.Observed)
}

// writeLoadReport writes the report files the formats ask for.
func writeLoadReport(dir string, formats []domain.OutputFormat, out *usecase.LoadOutcome) error {
	err := os.MkdirAll(dir, 0o750)
	if err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}

	files, err := renderReportFiles(formats, out)
	if err != nil {
		return err
	}

	for name, data := range files {
		err = os.WriteFile(filepath.Join(dir, name), data, 0o600)
		if err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}
