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
package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/codegen"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/llm"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/logger"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/notifier"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/router"
	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func main() {
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

	// ---- Multi-language code generation system ----.
	appLogger := logger.New()
	codeGenAgent := initCodeGenerationAgent(appLogger)

	// ---- Use Cases: assembled from ports, know nothing about concrete adapters ----.
	services := httpapi.Services{
		Projects:       usecase.NewProjectService(store),
		Contracts:      usecase.NewContractService(store, store, contractEngine, llmGateway),
		Mocks:          usecase.NewMockService(store, store, store, contractEngine, llmGateway),
		Providers:      usecase.NewProviderService(store, llmGateway),
		MockServing:    usecase.NewMockServingService(store, store, store, contractEngine),
		GraphQLServing: usecase.NewGraphQLServingService(store, store, graphQLEngine),
		Codegen:        usecase.NewCodegenService(store, codeGenerator),
		Logs:           usecase.NewLogService(store),
		CodeGenAgent:   codeGenAgent,
	}

	// ---- Interface Adapter: HTTP controllers + routing ----.
	handler := httpapi.NewRouter(&services)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout is deliberately long: LLM mock generation and requests
		// with artificial delay (DelayMs) may take up to a minute.
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("OpenAPI Mocker started on :%s (data: %s)", port, dataDir)
	log.Printf("UI:  http://localhost:%s", port)
	log.Printf("API: http://localhost:%s/api", port)

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
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
