# Known limitations

Plan for these before a production or shared setup.

## Storage and scaling

- The JSON-file store (`internal/adapter/repository/jsonstore`) is not suited
  for multiple replicas, so run one. For high availability, add an adapter over
  a real database that implements the same five interfaces from
  `internal/usecase/ports.go`; the usecase and HTTP layers need no changes.
- Tokens, share links and audit entries live in the same JSON data file.
- LLM provider API keys are stored in the data file in plaintext. For
  production, encrypt the volume or feed keys in through a Kubernetes Secret
  and a separately protected endpoint.

## Access control

- Access control is per project and token based. Project listing and
  creation, `POST /api/import`, `/api/llm-providers` and the mock server itself
  (which has its own auth simulation) are not covered, and project creation
  and import are not audited.
- There is no user directory or SSO; put the application behind your ingress
  auth if you need that.
- Share links open a JSON snapshot, not a dedicated read-only UI page.

## Observability

- Observability data is in memory: hit counts, metrics and captured bodies
  reset when the server restarts and are per replica. The stored request log
  keeps the latest 5000 entries without bodies.
- In distributed log mode the history lives in the external system. The UI
  shows only the latest 5000 entries of this process, and a restart empties
  that window. The rest of the data (projects, contracts, mocks) is still in
  the JSON store, so run one replica.

## Load testing

- Load testing runs inside the server process and shares its CPU with the mock
  it may be loading, so load another instance (or an external target) for
  figures you want to publish.
- Only HTTP/1.1, h2 and h2c targets are covered, not gRPC, WebSocket or
  GraphQL subscriptions.
- There is no distributed (multi-machine) load generation, and `load run`
  against `mock://` targets needs the server to be running (`--server`).

## Code generation

- Native generators cover Go, TypeScript, Python, Java, Rust and C# and
  produce skeletons, not finished services. Other languages need the
  `openapi` strategy (openapi-generator-cli) or a pipeline step; see
  [Code generation](code-generation.md).
- Code generation is not available in headless `serve` mode.

## Protocols

- GraphQL introspection (`__schema`, `__type`) is not implemented; export the
  SDL instead. See [Contracts](contracts.md).
- gRPC: `google.protobuf.Any` and `.proto` extensions are not supported, and
  the gRPC port has no access control or auth simulation. See
  [Streaming and gRPC](streaming-grpc.md).
