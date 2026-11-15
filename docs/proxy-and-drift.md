# Record & replay, drift and contract diffs

## Record & replay proxy

Set an upstream base URL and `proxyMode` on
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

## Contract drift detection

With `driftCheck` on, a `GET`/`HEAD` that a
mock rule or the contract example answered is replayed against the upstream
in the background (never delaying the mock). The live answer is compared
with the mock's: status, content type and the structure of JSON bodies
(`missing-in-live`, `extra-in-live`, `type-mismatch` with the JSON path;
values are ignored, arrays are compared by their first element), and it is
validated against the contract (`contract`). Reports are listed in the UI
and by `GET /api/projects/{id}/drift?limit=` (newest first, last 1000
kept), cleared with `DELETE`. Unsafe methods are never replayed.

## Contract-diff alerts

The diff of two contract versions
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

## CI check

`openapi-mocker diff OLD NEW [--fail-on-breaking]
[--format text|json]` compares two contract files (YAML or JSON) without
starting the server or touching its data. Exit status: `0` ok, `1` breaking
changes found with `--fail-on-breaking`, `2` unreadable file or contract.
For example, against the contract on the main branch:
`git show origin/main:openapi.yaml > /tmp/old.yaml &&
openapi-mocker diff /tmp/old.yaml openapi.yaml --fail-on-breaking`.
