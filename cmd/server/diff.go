package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Exit codes of the diff command.
const (
	exitOK       = 0
	exitBreaking = 1 // --fail-on-breaking and the new contract breaks the old one.
	exitUsage    = 2 // bad arguments, unreadable or invalid contracts.
)

const diffUsage = `Usage: openapi-mocker diff OLD NEW [--fail-on-breaking] [--format text|json]

Compares two OpenAPI contracts (YAML or JSON files) and lists the changes that
affect clients: removed operations, responses and fields, changed types, new
required parameters and fields (breaking), and compatible-looking changes that
can still surprise a client (warnings).

  --fail-on-breaking  exit with status 1 when the new contract has breaking changes
  --format text|json  output format (default text)

Exit status: 0 no failure, 1 breaking changes found with --fail-on-breaking,
2 usage error or a contract that cannot be read.
`

var errDiffUsage = errors.New("usage error")

type diffOptions struct {
	oldPath, newPath string
	failOnBreaking   bool
	format           string
}

// parseDiffArgs reads the arguments; flags may stand anywhere, so both
// "diff --fail-on-breaking a b" and "diff a b --fail-on-breaking" work.
func parseDiffArgs(args []string) (diffOptions, error) {
	opts := diffOptions{oldPath: "", newPath: "", failOnBreaking: false, format: "text"}

	var files []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if len(arg) < 2 || arg[0] != '-' { //nolint:mnd // "-x" is the shortest flag.
			files = append(files, arg)

			continue
		}

		used, err := applyDiffFlag(&opts, arg, args[i+1:])
		if err != nil {
			return opts, err
		}

		i += used
	}

	if opts.format != "text" && opts.format != "json" {
		return opts, fmt.Errorf("%w: --format must be text or json", errDiffUsage)
	}

	if len(files) != 2 { //nolint:mnd // OLD and NEW.
		return opts, fmt.Errorf("%w: want two files, OLD and NEW", errDiffUsage)
	}

	opts.oldPath, opts.newPath = files[0], files[1]

	return opts, nil
}

// applyDiffFlag applies one flag and returns how many following arguments it
// consumed.
func applyDiffFlag(opts *diffOptions, arg string, rest []string) (int, error) {
	switch arg {
	case "--fail-on-breaking":
		opts.failOnBreaking = true
	case "--format":
		if len(rest) == 0 {
			return 0, fmt.Errorf("%w: --format needs a value", errDiffUsage)
		}

		opts.format = rest[0]

		return 1, nil
	case "-h", "--help":
		return 0, errHelp
	default:
		v, ok := cutPrefix(arg, "--format=")
		if !ok {
			return 0, fmt.Errorf("%w: unknown flag %s", errDiffUsage, arg)
		}

		opts.format = v
	}

	return 0, nil
}

var errHelp = errors.New("help requested")

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}

	return "", false
}

// runDiff is the "openapi-mocker diff" command; it returns the exit status.
func runDiff(args []string, stdout, stderr io.Writer) int {
	opts, err := parseDiffArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, diffUsage)

		return exitOK
	}

	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, diffUsage)

		return exitUsage
	}

	oldRaw, err := os.ReadFile(opts.oldPath) //nolint:gosec // the paths are the command's arguments.
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return exitUsage
	}

	newRaw, err := os.ReadFile(opts.newPath) //nolint:gosec // the paths are the command's arguments.
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return exitUsage
	}

	changes, err := openapi.NewEngine().BreakingChanges(string(oldRaw), string(newRaw))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return exitUsage
	}

	if opts.format == "json" {
		err = writeDiffJSON(stdout, changes)
	} else {
		err = writeDiffText(stdout, &opts, changes)
	}

	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)

		return exitUsage
	}

	if opts.failOnBreaking && domain.CountBreaking(changes) > 0 {
		return exitBreaking
	}

	return exitOK
}

func writeDiffJSON(w io.Writer, changes []domain.ContractChange) error {
	if changes == nil {
		changes = []domain.ContractChange{}
	}

	breaking := domain.CountBreaking(changes)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	err := enc.Encode(map[string]any{
		"breaking": breaking, "warnings": len(changes) - breaking, "changes": changes,
	})
	if err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	return nil
}

func writeDiffText(w io.Writer, opts *diffOptions, changes []domain.ContractChange) error {
	fmt.Fprintf(w, "Comparing %s -> %s\n", opts.oldPath, opts.newPath)

	for _, sev := range []domain.ChangeSeverity{domain.SeverityBreaking, domain.SeverityWarning} {
		for i := range changes {
			ch := &changes[i]
			if ch.Severity != sev {
				continue
			}

			label := "WARNING "
			if sev == domain.SeverityBreaking {
				label = "BREAKING"
			}

			where := ch.Operation
			if ch.Location != "" {
				where += " (" + ch.Location + ")"
			}

			fmt.Fprintf(w, "%s  %s: %s\n", label, where, ch.Message)
		}
	}

	breaking := domain.CountBreaking(changes)
	if len(changes) == 0 {
		fmt.Fprintln(w, "No changes that affect clients.")

		return nil
	}

	_, err := fmt.Fprintf(w, "\n%d breaking change(s), %d warning(s)\n", breaking, len(changes)-breaking)
	if err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	return nil
}
