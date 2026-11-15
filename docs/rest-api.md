# REST API

| Method / Path | Purpose |
|---|---|
| `GET/POST /api/projects` | list / create project |
| `GET/PATCH/DELETE /api/projects/{id}` | project (PATCH changes `validationMode`, `stateMode`, `upstreamUrl`, `proxyMode`, `driftCheck`) |
| `GET/POST /api/projects/{id}/contract` | get / publish active contract (JSON body may include `format`: `graphql`/`yaml`/`json`/`mcp`/`proto`; auto-detected when omitted) |
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
| `GET /mock/{id}/graphql` | GraphQL subscriptions over WebSocket (`graphql-transport-ws`) |
| `GET /api/projects/{id}/grpc/services` | services, methods and sample messages of the published `.proto` contract |
| `GET /api/grpc/info` | whether the gRPC listener is on, its address, TLS and the project metadata key |
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
| `GET/POST /api/projects/{id}/load/plans` | list / save load plans (body: a plan file, YAML or JSON) |
| `GET/PUT/DELETE /api/projects/{id}/load/plans/{planId}` | read (`version`, `format=yaml`) / save a new version / delete a plan |
| `GET /api/projects/{id}/load/plans/{planId}/versions` | versions of a plan |
| `GET/POST /api/projects/{id}/load/runs` | list runs / start one (body: `{"planId": "...", "version": 2}`, `{"plan": "<plan file>"}`, or a plan file) |
| `GET/DELETE /api/projects/{id}/load/runs/{runId}` | a run with live progress while it is active / delete it |
| `POST /api/projects/{id}/load/runs/{runId}/abort` | stop an active run; the partial result is kept |
| `GET /api/projects/{id}/load/runs/{runId}/report` | the report (`format=json` (default), `html`, `csv` or `junit`) |
| `GET /api/projects/{id}/access/whoami` | the caller's role and identity |
| `GET/POST /api/projects/{id}/access/tokens` | list / create API tokens (admin) |
| `DELETE /api/projects/{id}/access/tokens/{tokenId}` | revoke a token |
| `GET/POST /api/projects/{id}/access/share-links` | list / create read-only share links (admin) |
| `DELETE /api/projects/{id}/access/share-links/{linkId}` | revoke a share link |
| `GET /api/projects/{id}/audit` | audit log (`limit`, `action`, `actor`, `since`) |
| `GET /api/share/{token}` | read-only snapshot for a share link |
| `ANY /mock/{projectId}/**` | the mock server itself (by project contract; SSE and WebSocket for scripted rules) |

```bash
curl -H "X-Mock-Scenario: error" http://localhost:8080/mock/<projectId>/users/1
```

Streaming and gRPC:

```bash
# Server-Sent Events (a rule with a "stream" script)
curl -N http://localhost:8080/mock/<projectId>/events

# gRPC (start the server with GRPC_PORT=9090 and publish a .proto contract)
grpcurl -plaintext -H 'x-mock-project: <projectId>' localhost:9090 list
grpcurl -plaintext -H 'x-mock-project: <projectId>' -d '{"id":"1"}' \
  localhost:9090 shop.v1.Shop/GetItem
```

MCP tool call via the mock JSON-RPC endpoint:

```bash
curl -X POST http://localhost:8080/mock/<projectId>/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_forecast","arguments":{"city":"Berlin"}}}'
```
