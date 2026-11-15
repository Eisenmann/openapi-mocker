package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const loadUsage = `Usage: openapi-mocker load COMMAND [arguments]

Commands:
  run PLAN        run a load plan and write a report
  generate        write a starter plan from a contract
  validate PLAN   check a plan file without sending any load

Run "openapi-mocker load COMMAND --help" for the options of a command.

Exit status: 0 pass, 1 a threshold failed or the run was aborted,
2 usage error or an invalid plan.
`

var errLoadUsage = errors.New("usage error")

// runLoad is the "openapi-mocker load" command; it returns the exit status.
func runLoad(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, loadUsage)

		return exitUsage
	}

	switch args[0] {
	case "run":
		return runLoadRun(args[1:], stdout, stderr)
	case "generate":
		return runLoadGenerate(args[1:], stdout, stderr)
	case "validate":
		return runLoadValidate(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, loadUsage)

		return exitOK
	default:
		fmt.Fprintf(stderr, "error: unknown load command %q\n\n%s", args[0], loadUsage)

		return exitUsage
	}
}

// parseInterspersed parses flags that may stand before, between or after the
// positional arguments, and returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)

	var positional []string

	for {
		err := fs.Parse(args)
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}

			return nil, fmt.Errorf("%w: %w", errLoadUsage, err)
		}

		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}

		positional = append(positional, args[0])
		args = args[1:]
	}
}

// loadFail prints an error with the usage of the command and returns the
// usage exit status.
func loadFail(stderr io.Writer, usage string, err error) int {
	fmt.Fprintf(stderr, "error: %v\n\n%s", err, usage)

	return exitUsage
}

const loadValidateUsage = `Usage: openapi-mocker load validate PLAN

Reads a plan file (YAML or JSON) and reports whether it is valid. No load is sent.
`

func runLoadValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)

	files, err := parseInterspersed(fs, args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, loadValidateUsage)

		return exitOK
	}

	if err == nil && len(files) != 1 {
		err = fmt.Errorf("%w: want one plan file", errLoadUsage)
	}

	if err != nil {
		return loadFail(stderr, loadValidateUsage, err)
	}

	plan, err := readPlanFile(files[0])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return exitUsage
	}

	fmt.Fprintf(stdout, "%s: ok (%s)\n", files[0], describePlan(plan))

	return exitOK
}

// readPlanFile reads and validates a plan file.
func readPlanFile(path string) (*domain.Plan, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the command's argument.
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}

	plan, err := usecase.ParsePlan(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	err = plan.Validate()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return plan, nil
}

func describePlan(p *domain.Plan) string {
	steps := 0
	for i := range p.Scenarios {
		steps += len(p.Scenarios[i].Steps)
	}

	return fmt.Sprintf("%q: %s model, %d scenarios, %d steps, %d thresholds, %s",
		p.Name, p.Load.Kind, len(p.Scenarios), steps, len(p.Thresholds), p.Load.TotalDuration())
}

// planDir is the directory plan data files are read relative to.
func planDir(planPath string) string {
	return filepath.Dir(planPath)
}
