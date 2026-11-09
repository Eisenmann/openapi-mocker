// Command server is the composition root of the application (the only place
// where the outer layers of the Clean Architecture are wired together):
// here and only here are concrete adapters (JSON store, kin-openapi, HTTP
// clients for LLMs, code generator) injected into usecase-service constructors
// via the interfaces (ports) that those services themselves declared. No
// usecase file imports any adapter package directly — only the reverse.
//
// Configuration is done via environment variables (convenient for Docker/K8s):
//
//	PORT      - HTTP port (default 8080)
//	DATA_DIR  - directory for persistent data (default ./data),
//	            mounted as a volume/PVC in Docker/K8s.
//	ALERT_WEBHOOK_URL - optional; breaking contract changes are posted there.
//
// "openapi-mocker diff OLD NEW [--fail-on-breaking]" compares two contract
// files and exits (see diff.go).
//
// "openapi-mocker mcp-stdio MANIFEST.json" serves an MCP manifest over the
// MCP stdio transport for editors and agents (see mcpstdio.go).
//
// ADMIN_TOKEN, when set, is a server-wide admin API token: it works in every
// project (also one whose own tokens are lost) and its changes are audited as "admin".
//
// LOG_CAPTURE_BODIES (default on) and LOG_CAPTURE_MAX_BYTES (default 16384)
// control whether and how much of each request and response body is kept in
// memory for the log detail view and the live stream. METRICS_TOKEN, when
// set, protects GET /metrics with a bearer token.
//
// LOG_MODE=local|distributed picks where request logs go: the data file (the
// default), or external systems (stdout JSON lines, an NDJSON HTTP endpoint,
// Elasticsearch/OpenSearch) with only a recent window kept in memory. See
// logging.go for LOG_SINKS, LOG_EXPORT_BODIES and the sink settings.
//
// MCP_REQUIRE_SESSION=true makes the HTTP MCP endpoint demand Mcp-Session-Id.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/codegen"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/grpcserver"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/logger"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/notifier"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/router"
	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// shutdownGrace is how long open requests and the log queue get at shutdown.
const shutdownGrace = 10 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "diff" {
		os.Exit(runDiff(os.Args[2:], os.Stdout, os.Stderr))
	}

	if len(os.Args) > 1 && os.Args[1] == "mcp-stdio" {
		os.Exit(runMCPStdio(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	if len(os.Args) > 1 {
		if code, handled := runSubcommand(os.Args[1], os.Args[2:]); handled {
			os.Exit(code)
		}
	}

	runServer()
}

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

// runSubcommand runs the serve, healthcheck and version commands.
func runSubcommand(name string, args []string) (code int, handled bool) {
	switch name {
	case "serve":
		return runServe(args, os.Stderr), true
	case "healthcheck":
		return runHealthcheck(args, os.Stderr), true
	case "version", "--version":
		_, _ = fmt.Fprintln(os.Stdout, "openapi-mocker", version)
		return exitOK, true
	default:
		return 0, false
	}
}

func runServer() {
	port := getEnv("PORT", "8080")
	dataDir := getEnv("DATA_DIR", "./data")

	a, err := buildApp(dataDir, false)
	if err != nil {
		log.Fatal(err)
	}

	httpServer := newHTTPServer(port, a.handler)
	logStartup(port, dataDir)

	serveErr := serveUntilSignal([]*http.Server{httpServer, a.grpc}, a.services.Logs, a.exporter)
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}

func logStartup(port, dataDir string) {
	log.Printf("OpenAPI Mocker started on :%s (data: %s)", port, dataDir)
	log.Printf("UI:  http://localhost:%s", port)
	log.Printf("API: http://localhost:%s/api", port)
}

// listenAndServe starts srv, with TLS when it was configured for it.
func listenAndServe(srv *http.Server) error {
	if grpcserver.IsTLS(srv) {
		return srv.ListenAndServeTLS("", "")
	}

	return srv.ListenAndServe()
}

// serveUntilSignal runs the servers until one fails or SIGINT/SIGTERM arrives,
// then drains requests and flushes the log exporter.
func serveUntilSignal(servers []*http.Server, logSvc *usecase.LogService, exporter *usecase.LogExporter) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serveUntil(ctx, servers, logSvc, exporter)
}

// serveUntil runs the servers until one fails or ctx ends, then drains
// requests and flushes the log exporter.
func serveUntil(
	ctx context.Context, servers []*http.Server, logSvc *usecase.LogService, exporter *usecase.LogExporter,
) error {
	servers = slices.DeleteFunc(servers, func(s *http.Server) bool { return s == nil })

	// Open log streams never end on their own; close them when shutdown starts.
	servers[0].RegisterOnShutdown(logSvc.CloseStreams)

	failed := make(chan error, len(servers))

	for _, srv := range servers {
		go func() { failed <- listenAndServe(srv) }()
	}

	var serveErr error

	select {
	case err := <-failed:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
		log.Print("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	for _, srv := range servers {
		err := srv.Shutdown(shutdownCtx)
		if err != nil {
			log.Printf("http shutdown: %v", err)
		}
	}

	if exporter != nil {
		err := exporter.Close(shutdownCtx)
		if err != nil {
			log.Printf("flushing logs: %v", err)
		}
	}

	return serveErr
}

// newHTTPServer builds the HTTP server around the router.
func newHTTPServer(port string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout is deliberately long: LLM mock generation and requests
		// with artificial delay (DelayMs) may take up to a minute.
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// initCodeGenerationAgent wires the multi-language code generation system:
// adapters, router, usecase, and agent.
func initCodeGenerationAgent(appLogger *logger.Logger) *agents.CodeGenerationAgent {
	// Adapters.
	goAdapter, err := codegen.NewGoNativeAdapter(codegen.GoConfig{
		ModulePath:  "generated",
		GoVersion:   "1.21",
		TemplateDir: "./templates/go",
	})
	if err != nil {
		log.Fatalf("failed to create Go adapter: %v", err)
	}

	tsAdapter := codegen.NewTypeScriptNativeAdapter()
	pyAdapter := codegen.NewPythonNativeAdapter()
	csAdapter := codegen.NewCSharpNativeAdapter()
	javaAdapter := codegen.NewJavaNativeAdapter()
	rustAdapter := codegen.NewRustNativeAdapter()

	openAPIAdapter := codegen.NewOpenAPIGeneratorAdapter(
		codegen.OpenAPIConfig{
			CLIPath: getEnv("OPENAPI_GENERATOR_CLI", "openapi-generator-cli.jar"),
			DefaultOptions: map[string]string{
				"enumPropertyNaming": "UPPERCASE",
			},
		},
		&codegen.OSCommandExecutor{},
	)

	cicdAdapter := codegen.NewCICDAdapter(codegen.PlatformGitHubActions)

	// Router.
	langRouter := router.NewDefaultLanguageRouter()

	// UseCase.
	adapters := []ports.CodeGeneratorPort{
		goAdapter, tsAdapter, pyAdapter, csAdapter, javaAdapter, rustAdapter, openAPIAdapter, cicdAdapter,
	}

	useCase := usecase.NewCodeGeneratorUseCase(
		adapters,
		openAPIAdapter, // fallback.
		langRouter,
		&usecase.DefaultSchemaValidator{},
		appLogger,
	)

	// Agent.
	return agents.NewCodeGenerationAgent(
		useCase,
		&agents.DefaultGenerationPlanner{},
		&agents.DefaultResultValidator{},
		notifier.New(),
	)
}

// newContractService assembles the contract use cases. Breaking-change alerts
// are logged, and posted to ALERT_WEBHOOK_URL when it is set.
func newContractService(
	store *jsonstore.Store, engine *openapi.Engine, gateway usecase.LLMGateway, validators usecase.FormatValidators,
) *usecase.ContractService {
	alertNotifier, err := notifier.NewWithWebhook(os.Getenv("ALERT_WEBHOOK_URL"))
	if err != nil {
		log.Fatalf("invalid ALERT_WEBHOOK_URL: %v", err)
	}

	return usecase.NewContractService(store, store, engine, gateway, validators).
		WithChangeAlerts(engine, alertNotifier.Alerts()).
		WithConverter(engine)
}
