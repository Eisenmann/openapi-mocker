package httpapi

import (
	"net/http"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/webui"
)

// NewRouter assembles a single http.Handler: the REST API (/api/...), the
// dynamic mock server (/mock/{projectId}/...), and the embedded web interface
// (everything else). Uses the Go 1.22 stdlib net/http.ServeMux with routes
// in "METHOD /path/{param}" form. The router knows nothing about how the
// services in Services are implemented — it only calls their methods.
func NewRouter(s *Services) http.Handler {
	a := &api{s: *s}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)

		if _, err := w.Write([]byte("ok")); err != nil {
			http.Error(w, "failed to write response", http.StatusInternalServerError)
		}
	})

	// --- projects ---.
	mux.HandleFunc("GET /api/projects", a.listProjects)
	mux.HandleFunc("POST /api/projects", a.createProject)
	mux.HandleFunc("GET /api/projects/{id}", a.getProject)
	mux.HandleFunc("DELETE /api/projects/{id}", a.deleteProject)

	// --- contracts ---.
	mux.HandleFunc("GET /api/projects/{id}/contract", a.getContract)
	mux.HandleFunc("POST /api/projects/{id}/contract", a.saveContract)
	mux.HandleFunc("GET /api/projects/{id}/contract/versions", a.listContractVersions)
	mux.HandleFunc("GET /api/projects/{id}/contract/versions/{version}", a.getContractVersion)
	mux.HandleFunc("POST /api/projects/{id}/contract/versions/{version}/rollback", a.rollbackContract)
	mux.HandleFunc("GET /api/projects/{id}/contract/diff", a.diffContract)
	mux.HandleFunc("POST /api/projects/{id}/contract/validate", a.validateContract)
	mux.HandleFunc("POST /api/projects/{id}/contract/generate", a.generateContract)
	mux.HandleFunc("GET /api/projects/{id}/endpoints", a.listEndpoints)

	// --- mocks ---.
	mux.HandleFunc("GET /api/projects/{id}/mocks", a.listMocks)
	mux.HandleFunc("POST /api/projects/{id}/mocks", a.createMock)
	mux.HandleFunc("POST /api/projects/{id}/mocks/generate", a.generateMock)
	mux.HandleFunc("PUT /api/mocks/{mockId}", a.updateMock)
	mux.HandleFunc("DELETE /api/mocks/{mockId}", a.deleteMock)

	// --- LLM provider ---.
	mux.HandleFunc("GET /api/llm-providers", a.listProvidersGlobal)
	mux.HandleFunc("POST /api/llm-providers", a.createProvider)
	mux.HandleFunc("PUT /api/llm-providers/{providerId}", a.updateProvider)
	mux.HandleFunc("DELETE /api/llm-providers/{providerId}", a.deleteProvider)
	mux.HandleFunc("POST /api/llm-providers/{providerId}/test", a.testProvider)
	mux.HandleFunc("GET /api/projects/{id}/llm-providers", a.listProvidersForProject)

	// --- codegen ---.
	mux.HandleFunc("GET /api/projects/{id}/codegen/server", a.codegenServer)
	mux.HandleFunc("GET /api/projects/{id}/codegen/client", a.codegenClient)
	mux.HandleFunc("POST /api/projects/{id}/codegen/agent", a.codegenAgent)

	// --- logs ---.
	mux.HandleFunc("GET /api/projects/{id}/logs", a.listLogs)

	// --- dynamic mock server ---.
	mux.HandleFunc("POST /mock/{projectId}/graphql", a.serveGraphQL)
	mux.HandleFunc("GET /api/projects/{id}/graphql/schema", a.graphQLSchema)
	mux.HandleFunc("GET /api/projects/{id}/graphql/operations", a.graphQLOperations)
	mux.HandleFunc("/mock/{projectId}/{path...}", a.serveMock)
	mux.HandleFunc("/mock/{projectId}", a.serveMockRoot)

	// --- web interface (SPA) ---.
	fileServer := http.FileServer(http.FS(webui.FS()))
	mux.Handle("/", spaFallback(fileServer))

	return withCORS(mux)
}

func spaFallback(fs http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mock/") {
			http.NotFound(w, r)
			return
		}

		fs.ServeHTTP(w, r)
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Mock-Scenario")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
