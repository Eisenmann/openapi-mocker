package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const mcpStdioUsage = `Usage: openapi-mocker mcp-stdio [--scenario NAME] MANIFEST.json
       openapi-mocker mcp-stdio [--scenario NAME] --project ID

Runs an MCP mock server on stdin/stdout (newline-delimited JSON-RPC, the MCP
stdio transport), so an editor or agent can start it as a local MCP server:

  { "command": "openapi-mocker", "args": ["mcp-stdio", "weather-mcp.json"] }

The server answers from an MCP manifest file, or from the active contract of a
project in DATA_DIR (then the project's request validation mode applies).
--scenario selects the mockResponses scenario for every call. Diagnostics go to
stderr; stdout carries protocol messages only.

Exit status: 0 on end of input, 1 on an I/O failure, 2 on a usage error or an
unreadable manifest.
`

const (
	exitIOFailure = 1
	// maxStdioLine is the longest accepted request line.
	maxStdioLine = 4 << 20
	// stdioProject is the project id of a manifest file served from disk.
	stdioProject = "stdio"
)

var errStdioUsage = errors.New("usage error")

type stdioOptions struct {
	scenario string
	project  string
	file     string
}

func parseStdioArgs(args []string) (stdioOptions, error) {
	opts := stdioOptions{scenario: "", project: "", file: ""}

	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--scenario", "--project":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%w: %s needs a value", errStdioUsage, arg)
			}

			i++

			if arg == "--scenario" {
				opts.scenario = args[i]
			} else {
				opts.project = args[i]
			}
		default:
			if len(arg) > 1 && arg[0] == '-' {
				return opts, fmt.Errorf("%w: unknown flag %s", errStdioUsage, arg)
			}

			if opts.file != "" {
				return opts, fmt.Errorf("%w: only one manifest file is allowed", errStdioUsage)
			}

			opts.file = arg
		}
	}

	if (opts.file == "") == (opts.project == "") {
		return opts, fmt.Errorf("%w: give either a manifest file or --project", errStdioUsage)
	}

	return opts, nil
}

// fileContracts serves one manifest file as the active contract of a project.
type fileContracts struct{ raw string }

func (f fileContracts) AddVersion(_, _, _, _ string) *domain.Contract { return nil }

func (f fileContracts) GetActive(projectID string) (*domain.Contract, error) {
	return &domain.Contract{
		ID: "", ProjectID: projectID, Format: usecase.FormatMCP, Raw: f.raw, Version: 1,
		Source: "file", CreatedAt: time.Time{},
	}, nil
}

func (f fileContracts) GetVersion(projectID string, _ int) (*domain.Contract, error) {
	return f.GetActive(projectID)
}

func (f fileContracts) ListVersions(string) []*domain.Contract { return nil }

// discardLogs drops request logs: a stdio server has no log viewer.
type discardLogs struct{}

func (discardLogs) Add(*domain.RequestLog)                    {}
func (discardLogs) ListLogs(string, int) []*domain.RequestLog { return nil }

// newStdioService builds the serving service for the options and returns it
// with the project id to serve.
func newStdioService(opts *stdioOptions) (*usecase.MCPServingService, string, error) {
	if opts.project != "" {
		store, err := jsonstore.New(getEnv("DATA_DIR", "./data"))
		if err != nil {
			return nil, "", fmt.Errorf("open data dir: %w", err)
		}

		svc := usecase.NewMCPServingService(store, store, mcp.NewEngine(), usecase.WithProjects(store))

		_, err = svc.Tools(opts.project)
		if err != nil {
			return nil, "", fmt.Errorf("project %q: %w", opts.project, err)
		}

		return svc, opts.project, nil
	}

	raw, err := os.ReadFile(opts.file) //nolint:gosec // the path is the command's argument.
	if err != nil {
		return nil, "", fmt.Errorf("read manifest: %w", err)
	}

	engine := mcp.NewEngine()

	err = engine.ParseAndValidate(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid manifest %s: %w", opts.file, err)
	}

	return usecase.NewMCPServingService(fileContracts{raw: string(raw)}, discardLogs{}, engine), stdioProject, nil
}

// runMCPStdio is the mcp-stdio command; it returns the process exit code.
func runMCPStdio(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseStdioArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n\n%s", err, mcpStdioUsage)
		return exitUsage
	}

	svc, project, err := newStdioService(&opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-stdio: %v\n", err)
		return exitUsage
	}

	err = serveStdio(svc, project, opts.scenario, stdin, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-stdio: %v\n", err)
		return exitIOFailure
	}

	return exitOK
}

// serveStdio answers one JSON-RPC message per input line until the input ends.
func serveStdio(
	svc *usecase.MCPServingService, project, scenario string, in io.Reader, out, errOut io.Writer,
) error {
	_, _ = fmt.Fprintf(errOut, "mcp-stdio: serving %s on stdin/stdout\n", project)

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), maxStdioLine) //nolint:mnd // initial buffer size.

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		reply := answerLine(svc, project, scenario, line, errOut)
		if reply == nil {
			continue
		}

		_, err := out.Write(append(reply, '\n'))
		if err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}

	err := sc.Err()
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}

	return nil
}

// answerLine returns the single-line response to one request line (nil for a
// notification). A failing manifest becomes a JSON-RPC internal error.
func answerLine(svc *usecase.MCPServingService, project, scenario string, line []byte, errOut io.Writer) []byte {
	res, err := svc.Handle(project, append([]byte(nil), line...), scenario)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "mcp-stdio: %v\n", err)

		return internalErrorReply(line, err)
	}

	if res.DelayMs > 0 {
		time.Sleep(time.Duration(res.DelayMs) * time.Millisecond)
	}

	if res.Body == nil {
		return nil
	}

	var compact bytes.Buffer

	err = json.Compact(&compact, res.Body)
	if err != nil {
		return res.Body
	}

	return compact.Bytes()
}

// internalErrorReply builds a JSON-RPC -32603 response for the request line.
func internalErrorReply(line []byte, cause error) []byte {
	var req struct {
		ID json.RawMessage `json:"id"`
	}

	_ = json.Unmarshal(line, &req)

	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil
	}

	out, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": req.ID,
		"error": map[string]any{"code": -32603, "message": cause.Error()},
	})
	if err != nil {
		return nil
	}

	return out
}
