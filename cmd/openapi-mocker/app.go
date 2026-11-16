package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/codegen"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/exchange"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/llm"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/logger"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/mcp"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/statestore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/upstream"
	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// app is the wired application: the services, the HTTP handler and the
// optional gRPC listener.
type app struct {
	services *httpapi.Services
	handler  http.Handler
	store    *jsonstore.Store
	// grpc is nil when the gRPC listener is not configured.
	grpc     *http.Server
	exporter *usecase.LogExporter
	// loadRuns is nil in a lite app; the server stops its runs on shutdown.
	loadRuns *usecase.LoadRunService
	// engine reads contracts; the MCP control plane uses it to plan load tests.
	engine *openapi.Engine
}

// appOptions says which optional parts buildAppWith wires.
type appOptions struct {
	// agent wires the code generation agent.
	agent bool
	// load wires load testing.
	load bool
	// mockBase is the address of the server's own mock, the base of mock://
	// load targets; empty means http://127.0.0.1:$PORT (default 8080).
	mockBase string
	// logOut receives the stdout log sink; it is stderr where stdout carries a
	// protocol.
	logOut io.Writer
}

// buildApp wires the application on top of dataDir. A lite app (the serve
// command) leaves out the code generation agent, which a mock needs no part of,
// and load testing.
func buildApp(dataDir string, lite bool) (*app, error) {
	return buildAppWith(dataDir, &appOptions{agent: !lite, load: !lite, mockBase: "", logOut: os.Stdout})
}

// buildAppWith is buildApp with the optional parts chosen one by one.
//
//nolint:funlen // composition root: wiring only.
func buildAppWith(dataDir string, opts *appOptions) (*app, error) {
	// ---- Frameworks & Drivers / Interface Adapters: concrete implementations ----.
	store, err := jsonstore.New(dataDir) // implements all 5 repository ports.
	if err != nil {
		return nil, fmt.Errorf("initialize store (%s): %w", dataDir, err)
	}

	contractEngine := openapi.NewEngine()   // implements usecase.ContractEngine.
	llmGateway := llm.NewGateway()          // implements usecase.LLMGateway.
	codeGenerator := codegen.NewGenerator() // implements usecase.CodeGenerator.
	graphQLEngine := graphql.NewEngine()    // implements usecase.GraphQLEngine.
	mcpEngine := mcp.NewEngine()            // implements usecase.MCPEngine.
	protoEngine := protobuf.NewEngine()     // implements usecase.ProtoEngine.

	// ---- Multi-language code generation system ----.
	var codeGenAgent *agents.CodeGenerationAgent
	if opts.agent {
		codeGenAgent = initCodeGenerationAgent(logger.New())
	}

	// ---- Use Cases: assembled from ports, know nothing about concrete adapters ----.
	validators := usecase.FormatValidators{GraphQL: graphQLEngine, MCP: mcpEngine, Proto: protoEngine}

	stateStore, err := statestore.New(dataDir) // implements usecase.StateStore.
	if err != nil {
		return nil, fmt.Errorf("initialize state store (%s): %w", dataDir, err)
	}

	stateService := usecase.NewStateService(stateStore, store, contractEngine)
	contractService := newContractService(store, contractEngine, llmGateway, validators)
	projectService := usecase.NewProjectService(store)

	logHub, logExporter, err := newLogHub(store, opts.logOut)
	if err != nil {
		return nil, err
	}

	authService := usecase.NewAuthService(store, contractEngine, usecase.WithAuthLogs(logHub))
	mockServing := usecase.NewMockServingService(
		store, store, logHub, contractEngine, usecase.WithProjects(store), usecase.WithState(stateService),
		usecase.WithUpstream(upstream.New(0), store), usecase.WithAuth(authService),
	)
	mockService := usecase.NewMockService(store, store, store, contractEngine, llmGateway)
	services := httpapi.Services{
		Projects:       projectService,
		Contracts:      contractService,
		Mocks:          mockService,
		Exchange:       usecase.NewExchangeService(projectService, store, contractService, mockService, exchange.New()),
		Providers:      usecase.NewProviderService(store, llmGateway),
		MockServing:    mockServing,
		GraphQLServing: usecase.NewGraphQLServingService(store, logHub, graphQLEngine),
		MCPServing:     usecase.NewMCPServingService(store, logHub, mcpEngine, usecase.WithProjects(store)),
		State:          stateService,
		Auth:           authService,
		Codegen:        usecase.NewCodegenService(store, codeGenerator),
		Logs:           usecase.NewLogService(logHub),
		Drift:          usecase.NewDriftService(store),

		MCPRequireSession: os.Getenv("MCP_REQUIRE_SESSION") == "true",
		Access:            usecase.NewAccessService(store, store, store, usecase.WithAdminToken(os.Getenv("ADMIN_TOKEN"))),
	}

	// Assigned only when set: a nil *CodeGenerationAgent in the interface is not nil.
	if codeGenAgent != nil {
		services.CodeGenAgent = codeGenAgent
	}

	// Load testing is part of the full server only: a lite app serves the mock at the root.
	var loadRuns *usecase.LoadRunService

	if opts.load {
		mockBase := opts.mockBase
		if mockBase == "" {
			mockBase = "http://127.0.0.1:" + getEnv("PORT", "8080")
		}

		ls, loadErr := newLoadServices(context.Background(), dataDir, store, contractEngine, mockBase)
		if loadErr != nil {
			return nil, loadErr
		}

		services.LoadPlans, services.LoadRuns, loadRuns = ls.plans, ls.runs, ls.runs
	}

	grpcServer := setupGRPC(&services, store, protoEngine, mockServing, mockService)

	// ---- Interface Adapter: HTTP controllers + routing ----.
	return &app{
		services: &services, handler: httpapi.NewRouter(&services), store: store,
		grpc: grpcServer, exporter: logExporter, loadRuns: loadRuns, engine: contractEngine,
	}, nil
}
