package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const serveUsage = `Usage: openapi-mocker serve SPEC [--port N] [--validation MODE] [--watch] [--data-dir DIR]

Serves one contract file as a mock server, with no web UI and nothing to set
up first. SPEC is an OpenAPI (YAML or JSON), GraphQL, MCP or .proto file.

  openapi-mocker serve spec.yaml
  curl http://localhost:8080/users/1

The operations are served at the root of the server. Reserved paths:
/healthz (health check) and /__mocker (management API, for example
/__mocker/api/projects/default/verify to assert what a service sent to the
mock, and /__mocker/metrics). The project is called "default" there.

  --port N             port to listen on (default: $PORT, else 8080)
  --validation MODE    check requests against the contract: off (default),
                       warn or enforce
  --watch              reload SPEC when the file changes
  --data-dir DIR       keep the project in DIR between runs; by default it
                       lives in a temporary directory that is removed on exit

Set GRPC_PORT to also serve a .proto contract over gRPC. The environment
settings of the full server (LOG_MODE, LOG_SINKS, ADMIN_TOKEN, ...) apply.
Stop with Ctrl+C or SIGTERM.

Exit status: 0 after a clean stop, 1 when serving fails, 2 on a usage error
or a spec that cannot be read or is invalid.
`

const (
	// serveProjectName is the name of the project that holds the served spec.
	serveProjectName = "spec"
	// projectAlias stands for the served project's id under adminPrefix.
	projectAlias = "default"
	// adminPrefix is where the management API lives in serve mode; the mock
	// owns every other path.
	adminPrefix = "/__mocker"

	watchInterval = 500 * time.Millisecond
	maxPort       = 65535
	maxBannerOps  = 25
)

var errServeUsage = errors.New("usage error")

type serveOptions struct {
	spec       string
	port       string
	dataDir    string
	validation string
	watch      bool
}

// parseServeArgs reads the arguments; flags may stand anywhere and take their
// value as "--port 9090" or "--port=9090".
func parseServeArgs(args []string) (serveOptions, error) {
	opts := serveOptions{spec: "", port: getEnv("PORT", "8080"), dataDir: "", validation: "", watch: false}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if len(arg) < 2 || arg[0] != '-' { //nolint:mnd // "-x" is the shortest flag.
			if opts.spec != "" {
				return opts, fmt.Errorf("%w: only one spec file is allowed", errServeUsage)
			}

			opts.spec = arg

			continue
		}

		name, value, hasValue := strings.Cut(arg, "=")

		if name == "--watch" {
			opts.watch = true
			continue
		}

		if !hasValue {
			rest := args[i+1:]
			if len(rest) == 0 {
				return opts, fmt.Errorf("%w: %s needs a value", errServeUsage, name)
			}

			value = rest[0]
			i++
		}

		err := setServeFlag(&opts, name, value)
		if err != nil {
			return opts, err
		}
	}

	err := opts.validate()

	return opts, err
}

func setServeFlag(opts *serveOptions, name, value string) error {
	switch name {
	case "--port":
		opts.port = value
	case "--data-dir":
		opts.dataDir = value
	case "--validation":
		opts.validation = value
	default:
		return fmt.Errorf("%w: unknown flag %s", errServeUsage, name)
	}

	return nil
}

func (o *serveOptions) validate() error {
	if o.spec == "" {
		return fmt.Errorf("%w: give the spec file to serve", errServeUsage)
	}

	port, err := strconv.Atoi(o.port)
	if err != nil || port < 1 || port > maxPort {
		return fmt.Errorf("%w: --port must be a number from 1 to %d, got %q", errServeUsage, maxPort, o.port)
	}

	if o.validation != "" && !domain.ValidationMode(o.validation).Valid() {
		return fmt.Errorf("%w: --validation must be off, warn or enforce, got %q", errServeUsage, o.validation)
	}

	return nil
}

// runServe is the serve command; it returns the process exit code.
func runServe(args []string, stderr io.Writer) int {
	opts, err := parseServeArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n\n%s", err, serveUsage)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serveSpec(ctx, &opts, stderr)
}

// serveSpec serves the spec until ctx ends.
func serveSpec(ctx context.Context, opts *serveOptions, stderr io.Writer) int {
	raw, err := os.ReadFile(opts.spec) //nolint:gosec // G703: the path is the command's argument.
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serve: read spec: %v\n", err)
		return exitUsage
	}

	dataDir := opts.dataDir

	if dataDir == "" {
		dataDir, err = os.MkdirTemp("", "openapi-mocker-")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
			return exitIOFailure
		}

		defer os.RemoveAll(dataDir)
	}

	//nolint:contextcheck // wiring starts workers that are stopped by Shutdown, not by ctx.
	a, err := buildApp(dataDir, true)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitIOFailure
	}

	//nolint:contextcheck // publishing is a quick synchronous call.
	projectID, err := bootstrapSpec(a, string(raw), opts.validation)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitUsage
	}

	printBanner(stderr, a, opts, projectID)

	if opts.watch {
		go watchSpec(ctx, a, opts.spec, projectID, stderr)
	}

	httpServer := newHTTPServer(opts.port, serveHandler(a.handler, projectID))

	err = serveUntil(ctx, []*http.Server{httpServer, a.grpc}, a.services.Logs, a.exporter)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitIOFailure
	}

	return exitOK
}

// bootstrapSpec makes sure the project exists and serves raw, and returns its id.
func bootstrapSpec(a *app, raw, validation string) (string, error) {
	var project *domain.Project

	for _, p := range a.services.Projects.List() {
		if p.Name == serveProjectName {
			project = p
			break
		}
	}

	if project == nil {
		created, err := a.services.Projects.Create(serveProjectName, "Served from the command line")
		if err != nil {
			return "", fmt.Errorf("create project: %w", err)
		}

		project = created
	}

	err := publishSpec(a, project.ID, raw)
	if err != nil {
		return "", err
	}

	if validation != "" {
		_, err = a.services.Projects.SetValidationMode(project.ID, domain.ValidationMode(validation))
		if err != nil {
			return "", fmt.Errorf("set validation mode: %w", err)
		}
	}

	return project.ID, nil
}

// publishSpec publishes raw unless it already is the active contract.
func publishSpec(a *app, projectID, raw string) error {
	active, err := a.services.Contracts.GetActive(projectID)
	if err == nil && active.Raw == raw {
		return nil
	}

	_, err = a.services.Contracts.Publish(projectID, raw, "file")
	if err != nil {
		return fmt.Errorf("invalid spec: %w", err)
	}

	return nil
}

func printBanner(w io.Writer, a *app, opts *serveOptions, projectID string) {
	base := "http://localhost:" + opts.port

	_, _ = fmt.Fprintf(w, "openapi-mocker %s serving %s on %s\n", version, opts.spec, base)
	_, _ = fmt.Fprintf(w, "  health: %s/healthz\n  management API: %s%s/api/projects/%s/...\n",
		base, base, adminPrefix, projectAlias)

	endpoints, err := a.services.Contracts.ListEndpoints(projectID)
	if err != nil {
		return
	}

	for i, e := range endpoints {
		if i == maxBannerOps {
			_, _ = fmt.Fprintf(w, "  … and %d more\n", len(endpoints)-maxBannerOps)
			break
		}

		_, _ = fmt.Fprintf(w, "  %-7s %s\n", e.Method, e.Path)
	}
}

// watchSpec republishes the spec whenever the file changes. A change that does
// not parse is reported and the previous version keeps being served.
func watchSpec(ctx context.Context, a *app, path, projectID string, stderr io.Writer) {
	last := statKey(path)
	ticker := time.NewTicker(watchInterval)

	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := statKey(path)
		if now == last || now == "" {
			continue
		}

		last = now

		raw, err := os.ReadFile(path) //nolint:gosec // the path is the command's argument.
		if err == nil {
			//nolint:contextcheck // publishing is a quick synchronous call.
			err = publishSpec(a, projectID, string(raw))
		}

		if err != nil {
			_, _ = fmt.Fprintf(stderr, "serve: reload failed, keeping the previous spec: %v\n", err)
			continue
		}

		_, _ = fmt.Fprintf(stderr, "serve: reloaded %s\n", path)
	}
}

// statKey identifies a file version; "" when the file cannot be read.
func statKey(path string) string {
	info, err := os.Stat(path) //nolint:gosec // G703: the path is the command's argument.
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size())
}

// serveHandler puts the mock at the root of the server. Only /healthz and
// adminPrefix are the application's own; everything else is a mock request.
func serveHandler(next http.Handler, projectID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path

		switch {
		case p == "/healthz":
		case p == adminPrefix:
			writeServeInfo(w, projectID)
			return
		case strings.HasPrefix(p, adminPrefix+"/"):
			r = withPath(r, adminTarget(strings.TrimPrefix(p, adminPrefix), projectID))
		default:
			r = withPath(r, mockTarget(p, projectID))
		}

		next.ServeHTTP(w, r)
	})
}

// adminTarget maps a management path to the router's, resolving the project
// alias to the real id.
func adminTarget(rest, projectID string) string {
	alias := "/api/projects/" + projectAlias
	if rest == alias || strings.HasPrefix(rest, alias+"/") {
		return "/api/projects/" + projectID + strings.TrimPrefix(rest, alias)
	}

	return rest
}

func mockTarget(path, projectID string) string {
	target := "/mock/" + projectID
	if path == "/" {
		return target
	}

	return target + path
}

func withPath(r *http.Request, path string) *http.Request {
	out := r.Clone(r.Context())
	u := *r.URL
	u.Path, u.RawPath = path, ""
	out.URL = &u

	return out
}

func writeServeInfo(w http.ResponseWriter, projectID string) {
	w.Header().Set("Content-Type", "application/json")

	data, err := json.Marshal(map[string]string{
		"projectId": projectID, "alias": projectAlias,
		"verify": adminPrefix + "/api/projects/" + projectAlias + "/verify",
		"health": "/healthz",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, _ = w.Write(data)
}
