# Observability and test assertions

## Observability

Every mock request (REST, GraphQL, MCP, auth
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

## Test assertions

Tests can ask the mock what their service sent it.
`POST /api/projects/{id}/verify` checks the recorded calls (REST, GraphQL,
MCP, auth rejections) against a description and answers `200` with
`passed`, `count`, a one-line `message`, the `matches` (with request and
response bodies, newest first) and, when nothing matched, the `closest`
calls with what differed. Add `?strict=true` to get `409` on a failed
check, so a plain `curl --fail` can fail a test.
```json
{"method": "POST", "path": "/users/{id}", "query": {"dry": "true"},
 "headers": {"X-Trace": "t-1"}, "status": 201,
 "body": {"json": {"name": "ann"}}, "exactly": 1}
```

- *What matches:* `method`, `path` (segments written `{id}` or `*` match
    anything), `endpoint` (the contract operation: `/users/{id}`,
    `graphql`, or the MCP method), `query` and `headers` (all listed pairs
    must be present; names are case-insensitive), `status`, and `body` with
    exactly one of `equals`, `contains`, `regex` or `json`. A `json` body
    matches when the logged body holds at least those properties; arrays
    must have the same length and matching elements.
- *How often:* `atLeast`, `atMost` or `exactly` (`exactly: 0` means
    "never"); the default is at least once. `since` (RFC 3339) or `sinceId`
    (a log id) limit the check to later calls.
- *Isolation:* `DELETE /api/projects/{id}/calls` forgets the recorded calls
    (editor role) so the next test starts clean; the request log and hit
    counts are not touched. `GET /api/projects/{id}/calls?limit=` lists
    them with bodies.
- *Limits:* the latest 1000 calls per project are kept **in memory** (not
    in `db.json`, gone after a restart, in local and distributed mode
    alike). Body and header checks need body capture (`LOG_CAPTURE_BODIES`,
    default on; the check is refused when it is off) and see only what was
    captured: bodies are cut at `LOG_CAPTURE_MAX_BYTES`, binary bodies can
    not be matched, and `Authorization`, cookie and API-key headers are
    redacted. gRPC calls are recorded under `/package.Service/Method` with
    the request message as JSON, so `json` body checks work for them.
    Viewers may verify.
