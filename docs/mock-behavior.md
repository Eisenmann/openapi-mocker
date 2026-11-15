# Mock behaviour

## Response scenarios

Multiple response variants
for one endpoint (happy path, empty, error…), selected by the `X-Mock-Scenario` header.

## Fault injection

Per mock rule (REST), also in the mock editor, for
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

## Schema-example fallback

Used when a mock is not configured manually
or via LLM — the endpoint always responds with something valid.

## Request validation

Check incoming requests against the contract.
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

## Request matching

Pick a mock by the request instead of the
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

## Response templating

Put `{{placeholders}}` in mock rule bodies and
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

## Stateful mocks

Let `POST`, `PUT`, `PATCH` and `DELETE` change the
data that later `GET`s return, so a "create then fetch" flow can be
tested. Set per project (UI dropdown or `PATCH /api/projects/{id}` with
`{"stateMode": "off|memory|persisted"}`; default `off`; `openapi-mocker serve --stateful` turns it on for a served file). `memory` keeps
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
