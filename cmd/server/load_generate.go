package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const loadGenerateUsage = `Usage: openapi-mocker load generate (CONTRACT | --project ID) [options]

Writes a ready-to-run load plan for a contract: every GET/HEAD operation in a
"read" scenario, a ramp to --rate that is held for --hold, and thresholds on
latency, errors and contract violations.

  CONTRACT          an OpenAPI file (YAML or JSON)
  --project ID      use the active contract of a project in the data directory
  --data-dir DIR    data directory for --project (default $DATA_DIR or ./data)
  --target URL      base URL to load; default mock://PROJECT, which targets this server's mock
  --rate N          peak iterations per second (default 10)
  --ramp DUR        time to reach the rate (default 1m)
  --hold DUR        time to keep the rate (default 5m)
  --include-unsafe  add a scenario with POST/PUT/PATCH/DELETE operations
  --name NAME       plan name
  --format yaml|json  plan file format (default yaml)
  -o FILE           write the plan to FILE instead of standard output
`

type generateOptions struct {
	project, dataDir, target, name, format, out string
	rate                                        int
	ramp, hold                                  time.Duration
	includeUnsafe                               bool
}

func runLoadGenerate(args []string, stdout, stderr io.Writer) int {
	opts := new(generateOptions)

	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.StringVar(&opts.project, "project", "", "")
	fs.StringVar(&opts.dataDir, "data-dir", getEnv("DATA_DIR", "./data"), "")
	fs.StringVar(&opts.target, "target", "", "")
	fs.StringVar(&opts.name, "name", "", "")
	fs.StringVar(&opts.format, "format", "yaml", "")
	fs.StringVar(&opts.out, "o", "", "")
	fs.IntVar(&opts.rate, "rate", 0, "")
	fs.DurationVar(&opts.ramp, "ramp", 0, "")
	fs.DurationVar(&opts.hold, "hold", 0, "")
	fs.BoolVar(&opts.includeUnsafe, "include-unsafe", false, "")

	files, err := parseInterspersed(fs, args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, loadGenerateUsage)

		return exitOK
	}

	if err == nil {
		err = checkGenerateArgs(opts, files)
	}

	if err != nil {
		return loadFail(stderr, loadGenerateUsage, err)
	}

	data, err := generatePlanFile(opts, files)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return exitUsage
	}

	return writeGenerated(opts.out, data, stdout, stderr)
}

func checkGenerateArgs(opts *generateOptions, files []string) error {
	if opts.format != "yaml" && opts.format != "json" {
		return fmt.Errorf("%w: --format must be yaml or json", errLoadUsage)
	}

	if (len(files) == 1) == (opts.project != "") {
		return fmt.Errorf("%w: give either a CONTRACT file or --project", errLoadUsage)
	}

	if len(files) > 1 {
		return fmt.Errorf("%w: want one contract file", errLoadUsage)
	}

	return nil
}

func generatePlanFile(opts *generateOptions, files []string) ([]byte, error) {
	raw, err := generateContract(opts, files)
	if err != nil {
		return nil, err
	}

	ops, err := openapi.NewEngine().LoadOperations(raw)
	if err != nil {
		return nil, fmt.Errorf("read contract: %w", err)
	}

	gen := new(usecase.GenerateOptions)
	gen.Name = opts.name
	gen.ProjectID = opts.project
	gen.BaseURL = opts.target
	gen.IncludeUnsafe = opts.includeUnsafe
	gen.Rate = opts.rate
	gen.Ramp = opts.ramp
	gen.Hold = opts.hold

	if gen.ProjectID == "" && gen.BaseURL == "" {
		gen.BaseURL = "http://localhost:8080"
	}

	plan, err := usecase.GeneratePlan(ops, gen)
	if err != nil {
		return nil, fmt.Errorf("generate plan: %w", err)
	}

	if opts.format == "json" {
		return usecase.FormatPlanJSON(plan)
	}

	return usecase.FormatPlanYAML(plan)
}

func generateContract(opts *generateOptions, files []string) ([]byte, error) {
	if len(files) == 1 {
		raw, err := os.ReadFile(files[0])
		if err != nil {
			return nil, fmt.Errorf("read contract: %w", err)
		}

		return raw, nil
	}

	return storedContract(context.Background(), opts.dataDir, opts.project)
}

func writeGenerated(out string, data []byte, stdout, stderr io.Writer) int {
	if out == "" {
		_, err := stdout.Write(data)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)

			return exitUsage
		}

		return exitOK
	}

	err := os.WriteFile(out, data, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "error: write plan: %v\n", err)

		return exitUsage
	}

	fmt.Fprintf(stderr, "wrote %s\n", strings.TrimSpace(out))

	return exitOK
}
