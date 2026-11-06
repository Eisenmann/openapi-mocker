# OpenAPI Mocker

A self-contained Go application for a mock server based on OpenAPI contracts:
project management, contract upload/edit/generation, manually and
LLM-generated mock data, built-in web UI, and Go server/client code generation.
Builds into a single binary / single Docker image and runs equally well on a
desktop, in Docker, and in Kubernetes.

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
8. **GraphQL contracts**: publish an SDL document instead of OpenAPI; the mock
   server then serves the GraphQL schema with validation of queries against it
   (per the GraphQL spec — aliases, fragments, inline fragments, arguments and
   variables, including `__typename`), SDL export via
   `GET /api/projects/{id}/graphql/schema`, and automatic mock response
   generation shaped by the selection set. Subscriptions are answered as
   one-shot queries (a reasonable mock simplification). Note: the mock engine
   does **not** implement the GraphQL introspection system (`__schema`/`__type`);
   introspection queries return a clear error pointing at the SDL export
   endpoint. List fields honor a `limit` argument (clamped to a maximum of 20
   items); without one, a small default of 2 items is returned.
9. **MCP contracts**: publish a JSON MCP server manifest (a root `mcpServer`
   block with `name`, `version` and `tools`; see `examples/simple-mcp-server.json`)
   instead of an OpenAPI document; the mock server then speaks the MCP wire
   protocol (JSON-RPC 2.0) at `POST /mock/{id}/mcp`: `initialize`, `ping`,
   `tools/list` and `tools/call` with per-tool mock responses. Response
   scenarios work like REST mocking: send `X-Mock-Scenario` to pick among
   the tool declared `mockResponses`. A mock that is not already an MCP
   result (no `content`/`structuredContent`/`isError`) is wrapped into a valid
   one. JSON-RPC batches are supported and notifications get `202`. An external manifest layout
   (`serverInfo` + `tools` at the root) is accepted as well.
10. Go server and Go client code generation in one click (download as zip).
11. Additional features:
   - **Response scenarios** (`X-Mock-Scenario`) — multiple response variants
     for one endpoint (happy path, empty, error…), selected by a header.
   - **Record & replay proxy** — set an upstream base URL and `proxyMode` on
     the project (**Proxy & Drift** tab, or `PATCH /api/projects/{id}` with
     `upstreamUrl`, `proxyMode`, `driftCheck`). A request that no mock rule
     answers (neither `X-Mock-Scenario`, a `match` rule, a recording nor a
     default rule) is sent to the upstream, the request path being appended to
     its URL; query, body and headers (including `Authorization`) are passed on,
     `X-Mock-*` and hop-by-hop headers are not. `forward` returns the upstream
     answer; `record` also saves it as a mock rule (scenario `recorded`, served
     verbatim without `{{placeholders}}`) that answers exactly that request
     (method, path, query and body; JSON compared structurally) from then on.
     5xx answers, non-UTF-8 or larger-than-1 MB bodies and request bodies over
     64 KB are not recorded; `Set-Cookie`, `Date` and other connection headers
     are dropped. Recorded rules are ordinary rules: edit or delete them in
     **Mock Data**. Answers are marked `X-Mock-Source: proxy-forward` /
     `proxy-record`; an unreachable upstream gives `502` (`proxy-error`) and a
     request that comes back to the mock server through the upstream URL gives
     `508`. REST only (not GraphQL or MCP). Recorded bodies can contain secrets
     or personal data, and the server requests the configured URL (including
     private addresses): point it only at hosts you trust.
   - **Contract drift detection** — with `driftCheck` on, a `GET`/`HEAD` that a
     mock rule or the contract example answered is replayed against the upstream
     in the background (never delaying the mock). The live answer is compared
     with the mock's: status, content type and the structure of JSON bodies
     (`missing-in-live`, `extra-in-live`, `type-mismatch` with the JSON path;
     values are ignored, arrays are compared by their first element), and it is
     validated against the contract (`contract`). Reports are listed in the UI
     and by `GET /api/projects/{id}/drift?limit=` (newest first, last 1000
     kept), cleared with `DELETE`. Unsafe methods are never replayed.
   - **Fault injection** — per mock rule (REST), also in the mock editor, for
     testing client resilience: latency (`delayMs` plus a random
     `fault.jitterMs`), injected errors (`failRatePct`, with `fault.errorStatuses`
     such as `[429, 503]`, default a random 500-503), timeouts
     (`fault.timeoutRatePct`: the request is held for `fault.timeoutMs`,
     default 30 s, and then the connection is dropped), dropped connections
     (`fault.dropRatePct`: reset without any response) and malformed bodies
     (`fault.malformedRatePct` with `fault.malformedMode`: `garbage` = invalid
     JSON, `truncated` = half the body then the connection is dropped,
     `empty` = no body). Each request rolls once: the rates are exclusive
     outcomes (drop, timeout, error, malformed) and must add up to at most
     100; whatever is left is a normal response. Latency applies to every
     outcome. Limits: delay and jitter up to 120 s, timeout hold up to 150 s;
     invalid settings give `400`. Injected faults are marked in the request log
     (`fault`), by `X-Mock-Fault` and by `X-Mock-Source` (`chaos-injection`,
     `chaos-malformed`). For **MCP**, put a `"fault"` object on a tool (all its
     calls) or inside one of its `mockResponses` entries (that entry only, it
     replaces the tool's; like `match` the key is removed from the result):
     `{"delayMs": 200, "jitterMs": 50, "errorRatePct": 10, "errorStatuses": [503],
     "timeoutRatePct": 5, "timeoutMs": 5000, "dropRatePct": 5,
     "malformedRatePct": 5, "malformedMode": "truncated"}` (`failRatePct` is
     accepted for `errorRatePct`). It has the same fields, rules and limits as
     above; an injected error answers with the chosen HTTP status (default
     500-503) and a JSON-RPC `-32603` error, and unknown fields or invalid
     settings fail contract validation. Only `tools/call` is affected; for a
     batch the largest delay is applied and the first faulted call decides the
     fault. Not available for GraphQL yet.
   - **Schema-example fallback**, used when a mock is not configured manually
     or via LLM — the endpoint always responds with something valid.
   - **Request validation** — check incoming requests against the contract.
     Set per project (UI dropdown or `PATCH /api/projects/{id}` with
     `{"validationMode": "off|warn|enforce"}`; default `off`). For REST it
     validates path/query/header parameters, the request `Content-Type` and
     the JSON body against the OpenAPI operation; for MCP it validates
     `tools/call` arguments against the tool's `inputSchema`. `warn` serves
     the mock anyway, adds an `X-Mock-Validation` header and records the
     violations in the request log (for MCP the warnings are also attached to
     the `tools/call` result as `_meta["openapi-mocker/validationWarnings"]`);
     `enforce` answers `400` with `{"error": ..., "violations": [...]}` (REST)
     or JSON-RPC `-32602` with the violations in `error.data` (MCP). Security schemes are not checked.
   - **Request matching** — pick a mock by the request instead of the
     `X-Mock-Scenario` header. A REST rule can carry `match` conditions
     (`[{"source": "body|query|header|path", "name": "user.role", "op": "equals",
     "value": "admin"}]`; `source: body` takes a JSON path such as
     `items[0].id`; ops: `equals` (default), `not_equals`, `contains`, `regex`,
     `exists`, `not_exists`); it is picked when all its conditions hold.
     Priority: an explicit `X-Mock-Scenario` first, then the matching rule with
     the most conditions (the first created on a tie), then the unconditional
     default rule, then the schema example. A rule with conditions never acts as
     the default. Conditions are validated when the rule is saved (invalid ones
     give `400`; `contains` and `regex` need a non-empty value) and can be edited
     in the mock editor. `X-Mock-Scenario: default` selects the unconditional
     default rule, never a conditional one. Only the first value of a repeated
     query parameter or header is compared. For MCP, a tool's
     `mockResponses` entry may have a `"match"` key, either
     `{"city": "Oslo"}` (all equal) or the condition array
     `[{"name": "days", "op": "regex", "value": "^[1-9]"}]` over the tool
     arguments (`name` is a JSON path, and a backslash makes a character
     literal: `units\.temp` is the key `units.temp`); array
     conditions need a `value` unless the op is `exists`/`not_exists`. The key is
     removed from the result, an explicit scenario still wins, then the best
     matching entry (the most conditions; on a tie the lexically first key),
     then `default`. Malformed `match` fails contract validation.
   - **Response templating** — put `{{placeholders}}` in mock rule bodies and
     header values (REST) and in MCP tool `mockResponses`, so one mock reacts
     to the request instead of needing a rule per case. Available:
     `{{request.method}}`, `{{request.path}}`, `{{request.query.name}}`,
     `{{request.header.X-Name}}`, `{{request.body}}`, `{{request.body.user.name}}`
     / `{{request.body.items[0].id}}` (for MCP the body is the tool call
     arguments), `{{path.id}}`, `{{uuid}}`, `{{now}}` / `{{now +2h}}` /
     `{{now -3d}}`, `{{now.unix}}` / `{{timestamp}}`, `{{date}}`,
     `{{counter}}` / `{{counter orders}}` (per project, +1 on every use, kept in
     memory), and faker data: `{{faker.name}}`, `firstName`, `lastName`,
     `username`, `email`, `phone`, `city`, `country`, `company`, `word`,
     `sentence [n]`, `int [min max]`, `float [min max]`, `bool`,
     `pick a b c`. In a JSON body a placeholder inside a string is escaped, and
     one outside a string becomes its JSON value (`{"id": {{path.id}}}` gives
     a number, a missing request field gives `null`); in MCP results a string
     that is exactly one placeholder keeps the value's type. Unknown
     placeholders are left as written, so existing mocks keep working. Rules
     are saved with their placeholders and must be valid JSON once rendered.
     Schema-example fallbacks and stateful responses are not templated.
   - **Stateful mocks** — let `POST`, `PUT`, `PATCH` and `DELETE` change the
     data that later `GET`s return, so a "create then fetch" flow can be
     tested. Set per project (UI dropdown or `PATCH /api/projects/{id}` with
     `{"stateMode": "off|memory|persisted"}`; default `off`). `memory` keeps
     the data until restart, `persisted` also saves it to `state.json` in the
     data directory. REST conventions are inferred from the contract's paths:
     `GET /users` lists the collection, `POST /users` creates a resource (the
     `id` comes from the body, or is a sequential integer, or a UUID when the
     contract declares a string `id`; a duplicate id is `409`),
     `GET|PUT|PATCH|DELETE /users/{id}` read, replace, merge into or remove one
     (`404` when it does not exist). Success codes come from the contract
     (e.g. `201` for create, `204` for delete). Collections start empty;
     `GET /api/projects/{id}/state` shows them, `PUT
     /api/projects/{id}/state/users` seeds one from a JSON array, and `DELETE
     /api/projects/{id}/state[/users]` resets all or one. An explicit mock rule
     for an operation always wins over state, and operations that do not follow
     the conventions stay static. Query parameters (filters, paging) are not
     applied to lists.
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
| `GET/PATCH/DELETE /api/projects/{id}` | project (PATCH changes `validationMode`, `stateMode`, `upstreamUrl`, `proxyMode`, `driftCheck`) |
| `GET/POST /api/projects/{id}/contract` | get / publish active contract (JSON body may include `format`: `graphql`/`yaml`/`json`/`mcp`; auto-detected when omitted) |
| `GET /api/projects/{id}/contract/versions` | version history |
| `GET /api/projects/{id}/contract/versions/{version}` | contents of a specific version |
| `POST /api/projects/{id}/contract/versions/{version}/rollback` | roll back to a version (re-publishes it) |
| `GET /api/projects/{id}/contract/diff?from=X&to=Y` | line-by-line diff of two versions (to defaults to current) |
| `POST /api/projects/{id}/contract/validate` | validate contract (OpenAPI, GraphQL or MCP) |
| `POST /api/projects/{id}/contract/generate` | generate contract via LLM |
| `GET /api/projects/{id}/state` | collections of the stateful mock data |
| `PUT /api/projects/{id}/state/{collection}` | seed a collection from a JSON array of objects |
| `DELETE /api/projects/{id}/state[/{collection}]` | reset all collections, or one |
| `GET /api/projects/{id}/endpoints` | list contract operations (HTTP endpoints or GraphQL fields) |
| `GET /api/projects/{id}/graphql/schema` | SDL export of the published GraphQL contract |
| `GET /api/projects/{id}/graphql/operations` | GraphQL root fields (query/mutation/subscription) |
| `POST /mock/{id}/graphql` | execute a GraphQL query (JSON body, gets mock data) |
| `POST /mock/{id}/mcp` | MCP mock endpoint (JSON-RPC 2.0) |
| `GET /api/projects/{id}/mcp/tools` | list tools of the published MCP manifest |
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
| `GET/DELETE /api/projects/{id}/drift` | contract-drift reports of the proxy (list / clear) |
| `ANY /mock/{projectId}/**` | the mock server itself (by project contract) |

```bash
curl -H "X-Mock-Scenario: error" http://localhost:8080/mock/<projectId>/users/1
```

MCP tool call via the mock JSON-RPC endpoint:

```bash
curl -X POST http://localhost:8080/mock/<projectId>/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_forecast","arguments":{"city":"Berlin"}}}'
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