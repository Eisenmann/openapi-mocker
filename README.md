# OpenAPI Mocker

A self-contained Go application for a mock server based on OpenAPI contracts:
project management, contract upload/edit/generation, manually and
LLM-generated mock data, built-in web UI, and Go server/client code generation.
Builds into a single binary / single Docker image and runs equally well on a
desktop, in Docker, and in Kubernetes.

## Architecture

Built on **Clean Architecture** (Entities → Use Cases → Interface
Adapters → Frameworks & Drivers) with explicit dependency inversion: inner
layers declare interfaces (ports), outer layers implement them. Imports always
point inward: `domain ← usecase ← adapter ← main`. Neither `domain` nor
`usecase` imports `net/http`, `kin-openapi`, or anything about JSON storage
directly.

```
internal/
  domain/               Entities: pure business objects (Project, Contract,
                          MockRule, LLMProvider, RequestLog) + domain errors.
                          Zero dependencies, stdlib only.

  usecase/                Use Cases: business rules + ports (interfaces)
                          that the usecase layer needs from the outside world,
                          without knowing how they are implemented:
    ports.go                 ProjectRepository, ContractRepository,
                            MockRepository, ProviderRepository,
                            LogRepository, ContractEngine, LLMGateway,
                            CodeGenerator
    project_service.go       Project CRUD
    contract_service.go      publishing a contract (= live immediately),
                            versions, diff, rollback, generation via LLM
    mock_service.go          Mock rule CRUD, body generation via LLM
    provider_service.go      LLM provider CRUD, connection test
    mockserving_service.go   product core: builds a MockResponse from a
                            request (contract → mock rule → fallback to schema
                            example), without touching net/http
    codegen_service.go       server/client code generation
    log_service.go           request log

  adapter/                Interface Adapters: implementations of the usecase-
                            layer ports. Each package knows about a specific
                            library/protocol and does not let its types leak:
    repository/jsonstore/    all 5 repositories backed by a single JSON file
    openapi/                 ContractEngine on top of kin-openapi
    llm/                     LLMGateway: OpenAI-compatible APIs, Anthropic,
                            Google Gemini, custom HTTP provider
    codegen/                 CodeGenerator: Go server/client generation
    httpapi/                 HTTP controllers + routing (net/http, Go 1.22
                            ServeMux) - the only package (besides cmd/ and
                            webui/) that knows about net/http
    webui/                   embed.FS with static SPA assets (UI delivery)

cmd/server/main.go       Composition root: the only place where concrete
                          adapters are injected into usecase-service constructors
                          via interfaces. Want Postgres instead of a store or
                          a new LLM protocol? Edit only here and in the
                          corresponding internal/adapter/*; usecase/domain stays
                          untouched.

deploy/                   Dockerfile, docker-compose.yml, Kubernetes manifests
```

Boundary enforcement is easy to verify in practice: `internal/adapter/repository/jsonstore/store.go`
contains `var _ usecase.ProjectRepository = (*Store)(nil)` and equivalents for
the other 4 ports at the top of the file. Package compilation will fail if
`Store` stops implementing any of them. Same assertions exist for
`openapi.Engine` (ContractEngine), `llm.Gateway` (LLMGateway), and
`codegen.Generator` (CodeGenerator) - these implementations are never imported
from `internal/usecase`.

Two deliberate pragmatic compromises (without them, a Go adaptation of Clean
Architecture quickly drowns in boilerplate):

- **Domain entities carry json tags** (`internal/domain/*.go`). Formally this is
  a serialization detail, but `encoding/json` is part of the standard library
  rather than a swappable framework, so this is a dependency the domain has on
  no particular transport. HTTP controllers serialize domain structs directly
  instead of maintaining a separate DTO layer with manual field-by-field mapping.
- **`ContractEngine` re-parses the contract on every call** instead of caching
  `*openapi3.T`. For typical OpenAPI file sizes this is negligible and greatly
  simplifies cache invalidation when a new version is published.

## Features

1. Runs on desktop (`go run`/binary), in Docker, in Kubernetes.
2. Web UI in the browser (embedded SPA, nothing to deploy separately).
3. Projects: create, list, delete.
4. Contract: upload via file (YAML/JSON), manual editing in the browser,
   server-side validation against the OpenAPI 3.x spec.
   **Publication = live immediately**: the mock engine (`MockServingService`)
   always serves the latest saved version of the contract, so as soon as a
   contract is published, all endpoints described in it are immediately
   available at `/mock/{projectId}/...` — no separate "deploy" step.
   **Versioning and change history**: every save creates a new immutable
   version (history is never rewritten); in the UI you can view any old
   version, a line-by-line diff of a version against the current active one,
   and roll back to an old version (rollback also publishes a new version with
   that content — endpoints go live immediately).
5. Mock data: manual editing of body/status/headers/delay, or generation via
   LLM taking into account the endpoint's JSON Schema.
6. LLM providers: OpenAI, Azure OpenAI, Anthropic, Google Gemini, Ollama,
   vLLM, LM Studio, Groq, OpenRouter, Together, DeepSeek + arbitrary custom
   HTTP provider. Providers can be global or bound to a project.
7. OpenAPI contract generation from a text description via LLM + validation
   (including targeted validation of the mock body against the contract's
   response schema).
8. Go server and Go client code generation in one click (download as zip).
9. Additional features:
   - **Response scenarios** (`X-Mock-Scenario`) — multiple response variants
     for one endpoint (happy path, empty, error…), selected by a header.
   - **Delay simulation and chaos error injection** (`delayMs`,
     `failRatePct`) — for testing client resilience.
   - **Schema-example fallback**, used when a mock is not configured manually
     or via LLM — the endpoint always responds with something valid.
   - **Request log** to the mock server (method/path/status/time) right in the UI.
   - **"Try it" in the browser** — sending a request to the mock without
     Postman/curl.

## Quick start (desktop)

```bash
go mod tidy   # requires network access - downloads kin-openapi
go run ./cmd/server
# UI:  http://localhost:8080
```

## Docker

```bash
cd deploy
docker compose up --build
# UI:  http://localhost:8080
```

or manually:

```bash
docker build -f deploy/Dockerfile -t openapi-mocker:latest .
docker run -p 8080:8080 -v mocker-data:/data openapi-mocker:latest
```

## Kubernetes

```bash
kubectl apply -f deploy/k8s/00-namespace.yaml
kubectl apply -f deploy/k8s/
# (the image must be reachable by the cluster: docker build + push to your
#  registry, then fix image: in deploy/k8s/03-deployment.yaml)
kubectl -n openapi-mocker port-forward svc/openapi-mocker 8080:80
```

## REST API (summary)

| Method / Path | Purpose |
|---|---|
| `GET/POST /api/projects` | list / create project |
| `GET/DELETE /api/projects/{id}` | project |
| `GET/POST /api/projects/{id}/contract` | get / publish active contract |
| `GET /api/projects/{id}/contract/versions` | version history |
| `GET /api/projects/{id}/contract/versions/{version}` | contents of a specific version |
| `POST /api/projects/{id}/contract/versions/{version}/rollback` | roll back to a version (re-publishes it) |
| `GET /api/projects/{id}/contract/diff?from=X&to=Y` | line-by-line diff of two versions (to defaults to current) |
| `POST /api/projects/{id}/contract/validate` | validate contract |
| `POST /api/projects/{id}/contract/generate` | generate contract via LLM |
| `GET /api/projects/{id}/endpoints` | list contract operations |
| `GET/POST /api/projects/{id}/mocks` | list / create mock rules |
| `PUT/DELETE /api/mocks/{mockId}` | edit / delete a mock rule |
| `POST /api/projects/{id}/mocks/generate` | generate mock body via LLM |
| `GET/POST /api/llm-providers` | global LLM providers |
| `GET /api/projects/{id}/llm-providers` | providers available to the project |
| `PUT/DELETE /api/llm-providers/{id}` | edit / delete provider |
| `POST /api/llm-providers/{id}/test` | connection test |
| `GET /api/projects/{id}/codegen/server` | download Go server zip |
| `GET /api/projects/{id}/codegen/client` | download Go client zip |
| `GET /api/projects/{id}/logs` | recent mock requests |
| `ANY /mock/{projectId}/**` | the mock server itself (by project contract) |

```bash
curl -H "X-Mock-Scenario: error" http://localhost:8080/mock/<projectId>/users/1
```

## Known limitations / roadmap

- The JSON-file store (`internal/adapter/repository/jsonstore`) is not suited
  for multiple replicas — for HA, add an adapter over a real DB implementing
  the same 5 interfaces from `internal/usecase/ports.go`; the usecase and
  HTTP layers won't need changes.
- Code generation currently covers Go only; for other languages it makes sense
  to integrate `openapi-generator-cli` as a separate CI/CD step, or implement
  `usecase.CodeGenerator` with another adapter.
- No role model / authentication — the application is assumed to live behind a
  corporate VPN/ingress with its own auth
  (e.g. oauth2-proxy in front of the Ingress).
- LLM provider API keys are stored in the data file in plaintext — for
  production, encrypt them at rest or feed them in through a Kubernetes
  Secret and a separate protected endpoint.

## Development

```bash
go build ./...      # build everything
go vet ./...         # static analysis
go test ./...         # no tests yet — usecase layer is isolated by interfaces,
                       # which is exactly why they are easy to add: mocks for
                       # ProjectRepository/ContractEngine/... are simple stubs
                       # of 10-15 lines, no testcontainers/httptest needed.