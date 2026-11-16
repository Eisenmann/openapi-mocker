package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcpcontrol"
)

const mcpUsage = `Usage: openapi-mocker mcp [--port N] [--data-dir DIR]

Runs the MCP control plane on stdin/stdout (newline-delimited JSON-RPC, the MCP
stdio transport) and, in the same process, a mock server that the tools manage.
An agent in an editor or CI can then create projects, import contracts, add
mock rules and faults, read the request log, verify the calls the system under
test made and run load tests, with no web UI:

  { "command": "openapi-mocker", "args": ["mcp"] }

Every project is served at http://127.0.0.1:PORT/mock/PROJECT_ID. The tools
return that address, and opening http://127.0.0.1:PORT shows the web UI.

  --port N       port of the mock server on the local machine (default: a free
                 port, which the tools report)
  --data-dir DIR keep projects in DIR between runs; by default they live in a
                 temporary directory that is removed on exit

Diagnostics go to stderr; stdout carries protocol messages only. Stop with
Ctrl+C, SIGTERM or by closing stdin.

Exit status: 0 after a clean stop, 1 when the server cannot start, 2 on a usage
error.
`

const (
	// mcpShutdownGrace is how long the mock server gets to finish requests.
	mcpShutdownGrace = 5 * time.Second
	maxPortNumber    = 65535
)

var errMCPUsage = errors.New("usage error")

type mcpOptions struct {
	port    string
	dataDir string
}

func parseMCPArgs(args []string) (mcpOptions, error) {
	opts := mcpOptions{port: "0", dataDir: ""}

	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.port, "port", "0", "")
	fs.StringVar(&opts.dataDir, "data-dir", "", "")

	err := fs.Parse(args)
	if err != nil {
		return opts, fmt.Errorf("%w: %w", errMCPUsage, err)
	}

	if fs.NArg() > 0 {
		return opts, fmt.Errorf("%w: unexpected argument %q", errMCPUsage, fs.Arg(0))
	}

	port, err := strconv.Atoi(opts.port)
	if err != nil || port < 0 || port > maxPortNumber {
		return opts, fmt.Errorf("%w: --port must be a number from 0 to %d, got %q", errMCPUsage, maxPortNumber, opts.port)
	}

	return opts, nil
}

// runMCP is the mcp command; it returns the process exit code.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseMCPArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n\n%s", err, mcpUsage)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serveMCP(ctx, &opts, stdin, stdout, stderr)
}

// serveMCP runs the control plane and its mock server until stdin ends or ctx
// does.
func serveMCP(ctx context.Context, opts *mcpOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	dataDir := opts.dataDir

	if dataDir == "" {
		tmp, err := os.MkdirTemp("", "openapi-mocker-mcp-")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "mcp: %v\n", err)
			return exitIOFailure
		}

		defer os.RemoveAll(tmp)

		dataDir = tmp
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:"+opts.port)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp: cannot listen on 127.0.0.1:%s: %v\n", opts.port, err)
		return exitIOFailure
	}

	mockBase := "http://" + ln.Addr().String()

	//nolint:contextcheck // wiring starts workers that are stopped by Shutdown, not by ctx.
	a, err := buildAppWith(dataDir, &appOptions{agent: false, load: true, mockBase: mockBase, logOut: stderr})
	if err != nil {
		_ = ln.Close()

		_, _ = fmt.Fprintf(stderr, "mcp: %v\n", err)

		return exitIOFailure
	}

	httpServer := newHTTPServer("", a.handler)

	go serveMock(httpServer, ln, stderr)

	control := mcpcontrol.New(&mcpcontrol.Deps{
		Projects: a.services.Projects, Contracts: a.services.Contracts, Mocks: a.services.Mocks,
		Logs: a.services.Logs, LoadRuns: a.loadRuns, Operations: a.engine,
		MockBaseURL: mockBase, PersistState: opts.dataDir != "", Version: version,
	})

	_, _ = fmt.Fprintf(stderr, "mcp: control plane on stdin/stdout, mock server at %s (data: %s)\n", mockBase, dataDir)

	serveControl(ctx, control.Handle, stdin, stdout, stderr)

	stopLoadRuns(a.loadRuns) //nolint:contextcheck // it has its own shutdown deadline.

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mcpShutdownGrace)
	defer cancel()

	err = httpServer.Shutdown(shutdownCtx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp: stopping the mock server: %v\n", err)
	}

	flushStore(a.store)

	return exitOK
}

func serveMock(srv *http.Server, ln net.Listener, stderr io.Writer) {
	err := srv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintf(stderr, "mcp: mock server: %v\n", err)
	}
}

// serveControl answers one JSON-RPC message per input line until the input
// ends or ctx does. Requests run at the same time, so a tool that waits (for a
// load run) does not hold up the others; replies are written one at a time.
func serveControl(
	ctx context.Context, handle func(context.Context, []byte) []byte, in io.Reader, out, errOut io.Writer,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		writeMu sync.Mutex
	)

	reply := func(b []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()

		_, err := out.Write(append(b, '\n'))
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "mcp: write response: %v\n", err)
		}
	}

	lines := make(chan []byte)

	go readLines(ctx, in, errOut, lines)

loop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break loop
			}

			wg.Add(1)

			go func() {
				defer wg.Done()

				if res := handle(ctx, line); res != nil {
					reply(compactJSON(res))
				}
			}()
		case <-ctx.Done():
			break loop
		}
	}

	cancel()
	wg.Wait()
}

// readLines sends the non-empty lines of in to lines and closes it at the end.
func readLines(ctx context.Context, in io.Reader, errOut io.Writer, lines chan<- []byte) {
	defer close(lines)

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), maxStdioLine) //nolint:mnd // initial buffer size.

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		select {
		case lines <- append([]byte(nil), line...):
		case <-ctx.Done():
			return
		}
	}

	err := sc.Err()
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "mcp: read request: %v\n", err)
	}
}

// compactJSON puts a JSON value on one line, as the stdio transport requires.
func compactJSON(b []byte) []byte {
	var buf bytes.Buffer

	err := json.Compact(&buf, b)
	if err != nil {
		return b
	}

	return buf.Bytes()
}
