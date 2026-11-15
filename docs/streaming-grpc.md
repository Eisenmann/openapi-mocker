# Streaming and gRPC mocks

## Streaming and gRPC mocks

(WebSocket, Server-Sent Events, GraphQL
subscriptions, gRPC). All of it uses the Go standard library only.

- *Server-Sent Events.* A mock rule (or an `x-mock-stream` extension on an
    OpenAPI operation) with a `stream` script answers with
    `text/event-stream`. The script is a timeline:
    `{"retryMs":3000,"loop":false,"steps":[{"event":"tick","id":"1",
    "data":"{\"n\":{{counter}}}","repeat":5,"intervalMs":1000,"delayMs":0}]}`.
    `data` is a template, rendered again for every emission (`{{counter}}`,
    `{{uuid}}`, `{{now}}`, `{{faker.*}}`, `{{request.*}}`). The stream ends
    when the timeline ends (or never, with `loop`) or when the client
    disconnects.
- *WebSocket.* A mock rule with a `webSocket` script (or an
    `x-mock-websocket` extension; `{}` declares a plain echo endpoint)
    answers upgrade requests: `{"onConnect":[{"data":"hello"}],
    "rules":[{"match":{"type":"contains|equals|regex|json","pattern":"ping"},
    "reply":[{"data":"pong"}],"close":false}],"fallback":"echo|ignore|close",
    "pingIntervalMs":0,"closeAfterMs":0,"loop":false}`. Rules are tried in
    order for every client message; `json` matches when every top-level field
    of the pattern is present with the same value. A plain `GET` to a
    WebSocket-only operation returns `426 Upgrade Required`. The
    implementation is a built-in RFC 6455 server (fragmentation, ping/pong,
    close codes, subprotocol negotiation, 1 MiB message limit).
- *Scripts in the contract.* Put `x-mock-stream` / `x-mock-websocket` on
    an operation in the OpenAPI document; the same JSON works in a mock rule,
    where it takes priority and can be edited in the UI (Mock Data tab).
- *GraphQL subscriptions.* `GET /mock/{id}/graphql` upgrades to a
    WebSocket speaking the `graphql-transport-ws` protocol
    (`connection_init`, `subscribe`, `next`, `complete`, ping/pong). Each
    subscription streams generated results shaped by the selection set
    (the same mock data as queries). Tune it with the query parameters
    `events` (number of results, default 5, at most 10000) and
    `intervalMs` (spacing, default 1000, at most 60000), e.g. `/mock/{id}/graphql?events=10&intervalMs=500`.
    Queries and mutations sent over the socket produce one result and
    complete.
- *gRPC from `.proto` files.* Publish a `.proto` file as the contract
    (auto-detected; `format: "proto"`). Unary, server-streaming,
    client-streaming and bidirectional methods are mocked, with messages
    encoded in real protobuf wire format, generated from the schema
    (proto2/proto3, nested messages and enums, `oneof`, `map`, `repeated`,
    packed fields) and from the well-known types (`Timestamp`, `Duration`,
    `Empty`, `FieldMask`, wrappers, `Struct`/`Value`/`ListValue`).
    The listener is opt-in: set `GRPC_PORT=9090` (or `GRPC_ADDR=host:port`).
    It speaks gRPC over plain HTTP/2, or over TLS when `GRPC_TLS_CERT` and
    `GRPC_TLS_KEY` point to PEM files (then call it without `-plaintext`),
    and supports server reflection
    (`grpc.reflection.v1` and `v1alpha`), so `grpcurl -plaintext` and
    Postman work without the `.proto` file. Select the project with the
    `x-mock-project: <projectId>` metadata (optional when only one project
    defines the called service) and a scenario with `x-mock-scenario`.
    Mock rules use method `POST` and path `/pkg.Service/Method`; the body is
    the response message as JSON (templates can read the request message via
    `{{request.body.*}}`). For streaming responses, a JSON **array** body
    sends one message per element, or use a `stream` script (each step's
    `data` is one message). A bidirectional method answers every request
    message with one reply. HTTP status codes of a rule map to gRPC codes
    (404 NOT_FOUND, 400 INVALID_ARGUMENT, 401 UNAUTHENTICATED,
    403 PERMISSION_DENIED, 429 RESOURCE_EXHAUSTED, 503 UNAVAILABLE,
    5xx INTERNAL, ...), with the `message`/`error` field of the body as the
    status message; delay and fault injection apply as usual. Deadlines
    (`grpc-timeout`) and `gzip` request compression are honoured. gRPC calls
    appear in the request log. The "gRPC & Streams" tab lists the services,
    methods and sample messages (`GET /api/projects/{id}/grpc/services`) and
    has a tester for SSE, WebSocket and GraphQL subscriptions.
- *Several `.proto` files.* A contract may hold more than one file, so
    `import` statements between them work. Start each file with a marker
    line, using the name the imports refer to:
    `// ==== file: common/types.proto ====`. In the UI, selecting several
    `.proto` files in the Contract tab builds this for you. Imports of files
    of the contract and of the well-known types are checked (missing files,
    import cycles and a name defined twice are errors); a type is found by
    its full name wherever it is declared, without checking `import`
    visibility the way `protoc` does. Reflection serves each file under its
    own name with its imports.
- *Binary WebSocket frames.* A WebSocket step with `"encoding":"base64"`
    or `"hex"` sends its decoded `data` as a binary frame (`"text"` is the
    default). Templates work in the encoded text; steps without templates
    are checked when the rule is saved.
- *Both GraphQL WebSocket dialects.* `graphql-transport-ws` and the
    legacy `graphql-ws` (`subscriptions-transport-ws`: `start`, `stop`,
    `data`, `ka`, `connection_terminate`) are negotiated from the client's
    subprotocols. Whether an operation is a subscription comes from parsing
    the document, not from a keyword search.
- *Limits.* WebSocket replies sent without `encoding` are text frames.
    `google.protobuf.Any` and `.proto` extensions are not supported, and
    reflection does not serve extensions. The gRPC port has no access
    control or auth simulation (like the HTTP mock server, which has its
    own auth simulation). A stream script on a gRPC method that does not
    stream responses is rejected when the rule is saved.
