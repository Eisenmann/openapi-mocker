package main

import (
	"fmt"
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
}

// buildApp wires the application on top of dataDir. A lite app (the serve
// command) leaves out the code generation agent, which a mock needs no part of.
//
//nolint:funlen // composition root: wiring only.
func buildApp(dataDir string, lite bool) (*app, error) {
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
	if !lite {
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

	logHub, logExporter, err := newLogHub(store, os.Stdout)
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

	grpcServer := setupGRPC(&services, store, protoEngine, mockServing, mockService)

	// ---- Interface Adapter: HTTP controllers + routing ----.
	return &app{
		services: &services, handler: httpapi.NewRouter(&services), store: store,
		grpc: grpcServer, exporter: logExporter,
	}, nil
}
