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
   `tools/list`, `tools/call` with per-tool mock responses, plus resources
   and prompts (below). Response
   scenarios work like REST mocking: send `X-Mock-Scenario` to pick among
   the tool declared `mockResponses`. A mock that is not already an MCP
   result (no `content`/`structuredContent`/`isError`) is wrapped into a valid
   one. JSON-RPC batches are supported and notifications get `202`. An external manifest layout
   (`serverInfo` + `tools` at the root) is accepted as well.
   - **Tool metadata.** A tool may carry `title`, `outputSchema` and `annotations`
     (e.g. `readOnlyHint`); they are served in `tools/list`. When a tool has an
     `outputSchema`, its `tools/call` result also gets `structuredContent` (a
     text result holding a JSON object is parsed into it).
   - **Resources.** `resources` (`uri`, `name`, `title`, `description`, `mimeType`,
     `annotations` and `text` or base64 `blob`; `text` may also be any JSON
     value, served as compact JSON) answer `resources/list` and `resources/read`.
     `resourceTemplates` (`uriTemplate` such as `users://{id}/profile`, `{+path}`
     for slashes, `{?query}` lists) answer `resources/templates/list`, and a
     `resources/read` of a URI that fits a template returns its `text` with
     `{{id}}` filled in (without `text`, the variables as JSON). An unknown URI
     is error `-32002`. Subscriptions are not implemented.
   - **Prompts.** `prompts` (`name`, `title`, `description`, `arguments`,
     `messages` with `role` and a string or an MCP content object) answer
     `prompts/list` and `prompts/get`; `{{argument}}` is replaced in every
     string of the messages, and a missing `required` argument is `-32602`.
     A manifest may declare only resources and/or prompts; `initialize`
     advertises exactly the capabilities the manifest provides.
   - **Streamable HTTP.** `initialize` starts a session: the answer carries an
     `Mcp-Session-Id` header, and requests with an unknown or ended id get 404
     (`DELETE /mock/{id}/mcp` ends a session, 204). Sessions are optional; set
     `MCP_REQUIRE_SESSION=true` to answer 400 to requests without one (other
     than `initialize`). An unsupported `MCP-Protocol-Version` header is a 400.
     A client that accepts only `text/event-stream` (or sends
     `X-Mock-Stream: sse`) gets the answer as an SSE stream: one `message`
     event per response (so a batch is split), preceded by two
     `notifications/progress` events when the call has a `_meta.progressToken`.
     `GET /mock/{id}/mcp` with `Accept: text/event-stream` opens the server
     stream, which carries keep-alive comments and closes when the session is
     deleted. The auth simulation guards all three methods.
   - **stdio transport.** `openapi-mocker mcp-stdio MANIFEST.json` (or
     `mcp-stdio --project ID` for a stored project, using `DATA_DIR` and the
     project's validation mode) serves the same mock over stdin/stdout, so an
     editor can start it directly:
     `{"command": "openapi-mocker", "args": ["mcp-stdio", "weather-mcp.json"]}`.
     `--scenario NAME` selects the mock response scenario; logs go to stderr.
     Connection faults (drop, timeout, truncation) do not apply to stdio; a
     fault's delay does.
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
   - **Contract-diff alerts** — the diff of two contract versions
     (`GET /api/projects/{id}/contract/diff`, "Compare with current" in the UI)
     also lists, under `breaking`, the changes that affect clients of an
     OpenAPI contract. *Breaking*: a removed operation, successful response or
     response field, a response field that became optional, a changed type, a
     new required parameter or request field, a parameter or request field that
     became required, a request enum value that is no longer accepted.
     *Warning*: a parameter or request field the server no longer reads, a
     removed error response, a new response enum value. Publishing (or rolling
     back to) a version that breaks the previous one sends a
     `contract.breaking_changes` alert through the notifier: it is logged and,
     when `ALERT_WEBHOOK_URL` is set, posted there as
     `{"type", "text", "payload"}` (`text` is a one-line summary that Slack and
     Mattermost incoming webhooks show; `payload` has the project, the two
     versions and up to 50 changes, breaking first). Delivery is best effort and
     never blocks or fails publishing. GraphQL and MCP contracts are not
     analysed.
   - **CI check** — `openapi-mocker diff OLD NEW [--fail-on-breaking]
     [--format text|json]` compares two contract files (YAML or JSON) without
     starting the server or touching its data. Exit status: `0` ok, `1` breaking
     changes found with `--fail-on-breaking`, `2` unreadable file or contract.
     For example, against the contract on the main branch:
     `git show origin/main:openapi.yaml > /tmp/old.yaml &&
     openapi-mocker diff /tmp/old.yaml openapi.yaml --fail-on-breaking`.
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
   - **Auth simulation.** The `Auth simulation` panel of a project (or
     `GET/PUT/DELETE /api/projects/{id}/auth`) lets clients test their 401 and
     403 paths without an identity provider. Modes: `off` (default),
     `contract` (enforce the security requirements the contract declares per
     operation or globally: API key in header / query / cookie, HTTP bearer
     and basic, OAuth2 and OpenID Connect with required scopes; operations
     with `security: []` stay public) and `all` (every request needs a bearer
     token, basic credentials or an API key in `apiKeyHeader` /
     `?api_key=`). Missing credentials give `401` with a `WWW-Authenticate`
     challenge, unknown ones `401 invalid_token`, missing scopes
     `403 insufficient_scope`. Configure fixed `credentials` (tokens and API
     keys), `users` (basic auth and the password grant) and OAuth2 `clients`.
     Each can carry a **scenario** that decides what its holder gets: `expired`
     and `revoked` answer 401, `forbidden` and `insufficient_scope` answer 403.
     A client's scenario applies to the tokens it is issued. An empty `scopes`
     list means every scope; with no users or clients configured, any
     credentials are accepted. The **token endpoint** is
     `POST /mock/{projectId}<tokenPath>` (default `/oauth/token`) with the
     `client_credentials`, `password`, `authorization_code` (any code works,
     except `invalid`, `expired` and `used`) and `refresh_token` grants, form
     or JSON body, client credentials in the body or a Basic header. Issued
     tokens last `tokenTtlSeconds` (default 3600), then answer 401 as expired.
     Refresh tokens work once. Issued tokens live in memory and are lost on
     restart; the configured credentials are stored with the project and
     travel in bundles. Rejections show in the request log with source `auth`.
     GraphQL (`POST /mock/{projectId}/graphql`) and MCP
     (`POST /mock/{projectId}/mcp`) endpoints are guarded too. Their contracts
     carry no security requirements, so in either enabled mode the whole
     endpoint needs a bearer token, basic credentials or an API key, and the
     same scenarios apply (401 with `WWW-Authenticate`, 403). Per-operation
     scopes are not checked there. The token endpoint is served for these
     projects as well.
   - **Access control and audit.** Every project can have API tokens and
     read-only share links. A project is **open until its first token is
     created**; from then on every `/api/projects/{id}/...` and
     `/api/mocks/{id}` call needs `Authorization: Bearer omk_...`.
     Roles: `viewer` (read) < `editor` (change contracts, mocks, auth, export)
     < `admin` (manage tokens and share links). Secrets are shown once and only
     their SHA-256 hash is stored; tokens can expire (`ttlDays`) and be revoked.
     **Share links** (`oms_...`) are read-only: send them as `X-Share-Token`
     or `?share=` on GET requests; `GET /api/share/{token}` returns a JSON
     snapshot (project, contract, mocks). Share links cannot read the audit
     log or auth settings. The optional `ADMIN_TOKEN` environment variable is a
     superuser/recovery token that works for every project. The **audit log**
     (`GET /api/projects/{id}/audit?limit&action&actor&since`) records who
     changed contracts, mocks, settings, tokens and links, plus denied
     changes (`access.denied`); it keeps the latest 2000 entries per project
     and survives project deletion. The UI has an "Access and audit" panel and
     asks for a token once per project when the API answers 401.
   - **Observability.** Every mock request (REST, GraphQL, MCP, auth
     rejections) is logged with its contract operation (`endpoint`, e.g.
     `/users/{id}`), status and duration (including injected latency).
     - *Live log:* the **Try It / Logs** tab streams new requests as they
       arrive (Server-Sent Events, `GET /api/projects/{id}/logs/stream`; the
       event `id` is the log id, so a client that reconnects with
       `Last-Event-ID` gets what it missed). Click a row to see the captured
       request and response. Slow readers lose entries rather than slow the
       server (an `event: lag` reports how many).
     - *Body capture:* request and response headers and bodies are kept **in
       memory** for the latest 500 requests (not written to `db.json`) and are
       returned by the stream and `GET /api/projects/{id}/logs/{logId}`. The
       log list stays light. `Authorization`, `Cookie`, `Set-Cookie` and API-key
       headers are stored as `[redacted]`; bodies are stored as sent, so
       treat the log like the traffic it records. Bodies are cut to
       `LOG_CAPTURE_MAX_BYTES` (default 16384, max 262144); binary bodies are
       flagged and left out; `LOG_CAPTURE_BODIES=false` turns capture off.
       GraphQL and MCP entries show the JSON body without HTTP headers.
     - *Hit counts:* `GET /api/projects/{id}/stats` (and the "Hits per
       Endpoint" table) lists hits, errors, average and maximum latency per
       operation, counted since the server started; `DELETE` resets them.
       Requests no operation answered are counted as `unmatched`.
     - *Prometheus:* `GET /metrics` exposes `openapimocker_requests_total`,
       `openapimocker_endpoint_hits_total`,
       `openapimocker_request_duration_seconds` (histogram),
       `openapimocker_faults_total`, `openapimocker_request_violations_total`
       and `openapimocker_log_stream_subscribers`. Label sets are bounded, so
       a client cannot grow memory with made-up paths. Set `METRICS_TOKEN` to
       require `Authorization: Bearer <token>`; otherwise the endpoint is open
       (it lists project ids and API paths).
     - *Local or distributed logging:* `LOG_MODE=local` (the default) keeps
       the request log in the data file, as before. `LOG_MODE=distributed` is
       for server installations: entries are **shipped to external systems**
       and the process keeps only the latest 5000 in memory for the UI, so
       nothing is written to `db.json` and history lives in your log platform.
       Pick the destinations with `LOG_SINKS` (comma-separated; `stdout` is the
       default in distributed mode, and sinks can also be added in local mode):

       | Sink | What it does | Settings |
       |------|--------------|----------|
       | `stdout` | one JSON object per line on standard output, for a collector (Filebeat, Fluent Bit, Vector, Promtail/Alloy, Docker/Kubernetes log drivers) | none |
       | `http` | `POST` of newline-delimited JSON (`application/x-ndjson`), e.g. Logstash `http` input with `json_lines`, or Vector | `LOG_HTTP_URL`, optional `LOG_HTTP_TOKEN` (bearer) |
       | `elasticsearch` | the `_bulk` API of Elasticsearch or OpenSearch (`create` with the log id, so retries do not duplicate) | `LOG_ES_URL`, `LOG_ES_INDEX` (default `openapi-mocker-logs`, may contain `{date}` for daily indices), `LOG_ES_API_KEY` or `LOG_ES_USER` and `LOG_ES_PASSWORD` |

       Records use Elastic Common Schema field names (`@timestamp`,
       `log.level`, `http.request.method`, `url.path`,
       `http.response.status_code`, `event.duration` in nanoseconds) plus
       `mocker.*` fields (`project_id`, `endpoint`, `matched_rule`, `source`,
       `fault`, `violations`, `log_id`). Shipping is asynchronous and never
       slows a request: batches of up to 200 go out every second, a failing
       sink is retried three times, and when a queue overflows entries are
       dropped. Both are visible in `/metrics` as
       `openapimocker_log_export_events_total{sink,result="sent|failed|dropped"}`
       and `openapimocker_log_export_queue_length`. Bodies are shipped only
       with `LOG_EXPORT_BODIES=true` (sensitive headers stay redacted; the
       capture limits above apply). On `SIGTERM` the server stops accepting
       requests, closes live streams and flushes the queue (up to 10 s).
       Metrics are not shipped: scrape `/metrics` with Prometheus.
     Logs, stream, detail and stats need the viewer role once a project has
     tokens; share links cannot read them.
   - **Import and export.** `Import…` in the sidebar (or `POST /api/import`,
     the file as the request body or a multipart `file` field, optional
     `?name=`, up to 32 MB) creates a new project from:
     a **Postman** collection (v2.x), an **Insomnia** export (v4 JSON or v5
     YAML), a **HAR** file, an **OpenAPI 3.0/3.1 or Swagger 2** contract
     (JSON or YAML) or an **openapi-mocker bundle**. For collections and HAR
     files the contract is inferred (templated paths such as `/users/{userId}`,
     path and query parameters, request bodies and response schemas merged from
     the examples) and every saved response becomes a mock rule: the lowest 2xx
     is the default scenario, the other status codes are scenarios named
     `status-<code>`. HAR entries that are not API calls (assets, preflights,
     `304`) are skipped; `Set-Cookie`, `Authorization` and similar headers are
     never imported. Swagger 2.0 and OpenAPI 3.1 contracts are converted to
     OpenAPI 3.0.3 when published or validated (also in the Contract tab); the
     conversion note is shown, and 3.1-only keywords with no 3.0 equivalent
     (`prefixItems`, `if/then/else`, `webhooks`, ...) are dropped and listed.
     Export: `GET /api/projects/{id}/export?format=bundle|postman|insomnia`
     (`&history=1` puts every contract version into the bundle) and
     `GET /api/projects/{id}/contract/export?format=openapi3|openapi31|swagger2&as=yaml|json`.
     A **bundle** is a single JSON file with the project settings, the
     contract(s) and the mock rules, for sharing; import it to get a copy.
     Mock state, logs, drift reports, LLM providers and the upstream URL are not
     included. Recorded bodies in HAR files and collections can contain secrets:
     review them before sharing a bundle.
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
| `POST /mock/{id}/mcp` | MCP mock endpoint (JSON-RPC 2.0; JSON or SSE answer) |
| `GET /mock/{id}/mcp` | MCP server event stream (`Accept: text/event-stream`) |
| `DELETE /mock/{id}/mcp` | end an MCP session (`Mcp-Session-Id`) |
| `GET /api/projects/{id}/mcp/tools` | list tools of the published MCP manifest |
| `GET /api/projects/{id}/mcp/catalog` | tools, resources, resource templates and prompts |
| `GET/POST /api/projects/{id}/mocks` | list / create mock rules |
| `PUT/DELETE /api/mocks/{mockId}` | edit / delete a mock rule |
| `POST /api/projects/{id}/mocks/generate` | generate mock body via LLM |
| `GET/POST /api/llm-providers` | global LLM providers |
| `GET /api/projects/{id}/llm-providers` | providers available to the project |
| `PUT/DELETE /api/llm-providers/{id}` | edit / delete provider |
| `POST /api/llm-providers/{id}/test` | connection test |
| `GET /api/projects/{id}/codegen/server` | download Go server zip |
| `GET /api/projects/{id}/codegen/client` | download Go client zip |
| `GET /api/projects/{id}/logs` | recent mock requests (`limit`), without bodies |
| `GET /api/projects/{id}/logs/stream` | live request log (SSE; `Last-Event-ID` resumes) |
| `GET /api/projects/{id}/logs/{logId}` | one log entry with captured request and response |
| `GET/DELETE /api/projects/{id}/stats` | per-endpoint hit counts (read / reset) |
| `GET /metrics` | Prometheus metrics (optional `METRICS_TOKEN`) |
| `GET/PUT/DELETE /api/projects/{id}/auth` | read, replace or remove the auth simulation settings |
| `POST /mock/{projectId}/oauth/token` | simulated OAuth2 token endpoint (path configurable) |
| `POST /api/import` | create a project from a Postman / Insomnia / HAR / OpenAPI / Swagger file or a bundle |
| `GET /api/projects/{id}/export` | download a bundle, Postman or Insomnia collection (`format`, `history`) |
| `GET /api/projects/{id}/contract/export` | contract as OpenAPI 3.0 / 3.1 or Swagger 2 (`format`, `as`) |
| `GET/DELETE /api/projects/{id}/drift` | contract-drift reports of the proxy (list / clear) |
| `GET /api/projects/{id}/access/whoami` | the caller's role and identity |
| `GET/POST /api/projects/{id}/access/tokens` | list / create API tokens (admin) |
| `DELETE /api/projects/{id}/access/tokens/{tokenId}` | revoke a token |
| `GET/POST /api/projects/{id}/access/share-links` | list / create read-only share links (admin) |
| `DELETE /api/projects/{id}/access/share-links/{linkId}` | revoke a share link |
| `GET /api/projects/{id}/audit` | audit log (`limit`, `action`, `actor`, `since`) |
| `GET /api/share/{token}` | read-only snapshot for a share link |
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
- Access control is per project and token based. Project listing/creation,
  `POST /api/import`, `/api/llm-providers` and the mock server itself
  (which has its own auth simulation) are not covered, and project creation
  and import are not audited. There is no user directory or SSO; put the
  application behind your ingress auth if you need that. Share links open a
  JSON snapshot, not a dedicated read-only UI page. Tokens, links and audit
  entries live in the same JSON data file.
- In distributed mode the log history lives in the external system: the UI
  shows only the latest 5000 entries of this process, and a restart empties
  that window. The rest of the data (projects, contracts, mocks) is still in
  the JSON store, so run one replica.
- Observability data is in memory: hit counts, metrics and captured bodies
  reset when the server restarts and are per replica. The stored request log
  keeps the latest 5000 entries without bodies.
- LLM provider API keys are stored in the data file in plaintext — for
  production, encrypt them at rest or feed them in through a Kubernetes
  Secret and a separate protected endpoint.