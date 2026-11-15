# Contracts and mock data

## OpenAPI contracts

Upload via file (YAML/JSON), manual editing in the browser,
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

## Mock data

Manual editing of body/status/headers/delay, or generation via
LLM taking into account the endpoint's JSON Schema.

## LLM providers

OpenAI, Azure OpenAI, Anthropic, Google Gemini, Ollama,
vLLM, LM Studio, Groq, OpenRouter, Together, DeepSeek + arbitrary custom
HTTP provider. Providers can be global or bound to a project.

## Contract generation

OpenAPI contract generation from a text description via LLM + validation
(including targeted validation of the mock body against the contract's
response schema).

## GraphQL contracts

Publish an SDL document instead of OpenAPI; the mock
server then serves the GraphQL schema with validation of queries against it
(per the GraphQL spec — aliases, fragments, inline fragments, arguments and
variables, including `__typename`), SDL export via
`GET /api/projects/{id}/graphql/schema`, and automatic mock response
generation shaped by the selection set. Over HTTP `POST`, subscriptions are
answered as one-shot queries; over WebSocket they stream (see
[Streaming and gRPC mocks](streaming-grpc.md)). Note: the mock engine
does **not** implement the GraphQL introspection system (`__schema`/`__type`);
introspection queries return a clear error pointing at the SDL export
endpoint. List fields honor a `limit` argument (clamped to a maximum of 20
items); without one, a small default of 2 items is returned.

## MCP contracts

Publish a JSON MCP server manifest (a root `mcpServer`
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
