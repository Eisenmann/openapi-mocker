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
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/codegen"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/exchange"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/llm"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/logger"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/notifier"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/statestore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/router"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/upstream"
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

	runServer()
}

func runServer() {
	port := getEnv("PORT", "8080")
	dataDir := getEnv("DATA_DIR", "./data")

	// ---- Frameworks & Drivers / Interface Adapters: concrete implementations ----.
	store, err := jsonstore.New(dataDir) // implements all 5 repository ports.
	if err != nil {
		log.Fatalf("failed to initialize store (%s): %v", dataDir, err)
	}

	contractEngine := openapi.NewEngine()   // implements usecase.ContractEngine.
	llmGateway := llm.NewGateway()          // implements usecase.LLMGateway.
	codeGenerator := codegen.NewGenerator() // implements usecase.CodeGenerator.
	graphQLEngine := graphql.NewEngine()    // implements usecase.GraphQLEngine.
	mcpEngine := mcp.NewEngine()            // implements usecase.MCPEngine.

	// ---- Multi-language code generation system ----.
	codeGenAgent := initCodeGenerationAgent(logger.New())

	// ---- Use Cases: assembled from ports, know nothing about concrete adapters ----.
	validators := usecase.FormatValidators{GraphQL: graphQLEngine, MCP: mcpEngine}

	stateStore, err := statestore.New(dataDir) // implements usecase.StateStore.
	if err != nil {
		log.Fatalf("failed to initialize state store (%s): %v", dataDir, err)
	}

	stateService := usecase.NewStateService(stateStore, store, contractEngine)
	contractService := newContractService(store, contractEngine, llmGateway, validators)
	projectService := usecase.NewProjectService(store)

	logHub, logExporter, err := newLogHub(store, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}

	authService := usecase.NewAuthService(store, contractEngine, usecase.WithAuthLogs(logHub))
	mockService := usecase.NewMockService(store, store, store, contractEngine, llmGateway)
	services := httpapi.Services{
		Projects:  projectService,
		Contracts: contractService,
		Mocks:     mockService,
		Exchange:  usecase.NewExchangeService(projectService, store, contractService, mockService, exchange.New()),
		Providers: usecase.NewProviderService(store, llmGateway),
		MockServing: usecase.NewMockServingService(
			store, store, logHub, contractEngine, usecase.WithProjects(store), usecase.WithState(stateService),
			usecase.WithUpstream(upstream.New(0), store), usecase.WithAuth(authService),
		),
		GraphQLServing: usecase.NewGraphQLServingService(store, logHub, graphQLEngine),
		MCPServing:     usecase.NewMCPServingService(store, logHub, mcpEngine, usecase.WithProjects(store)),
		State:          stateService,
		Auth:           authService,
		Codegen:        usecase.NewCodegenService(store, codeGenerator),
		Logs:           usecase.NewLogService(logHub),
		Drift:          usecase.NewDriftService(store),
		CodeGenAgent:   codeGenAgent,

		MCPRequireSession: os.Getenv("MCP_REQUIRE_SESSION") == "true",
		Access:            usecase.NewAccessService(store, store, store, usecase.WithAdminToken(os.Getenv("ADMIN_TOKEN"))),
	}

	// ---- Interface Adapter: HTTP controllers + routing ----.
	handler := httpapi.NewRouter(&services)

	httpServer := newHTTPServer(port, handler)
	logStartup(port, dataDir)

	serveErr := serveUntilSignal(httpServer, services.Logs, logExporter)
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}

func logStartup(port, dataDir string) {
	log.Printf("OpenAPI Mocker started on :%s (data: %s)", port, dataDir)
	log.Printf("UI:  http://localhost:%s", port)
	log.Printf("API: http://localhost:%s/api", port)
}

// serveUntilSignal runs the server until it fails or SIGINT/SIGTERM arrives,
// then drains requests and flushes the log exporter.
func serveUntilSignal(srv *http.Server, logSvc *usecase.LogService, exporter *usecase.LogExporter) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Open log streams never end on their own; close them when shutdown starts.
	srv.RegisterOnShutdown(logSvc.CloseStreams)

	failed := make(chan error, 1)

	go func() { failed <- srv.ListenAndServe() }()

	var serveErr error

	select {
	case err := <-failed:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
		log.Print("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	err := srv.Shutdown(shutdownCtx)
	if err != nil {
		log.Printf("http shutdown: %v", err)
	}

	if exporter != nil {
		err = exporter.Close(shutdownCtx)
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
