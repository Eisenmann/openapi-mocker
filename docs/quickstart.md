# Quick start

## Desktop

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

## Sandbox: `serve` and the CI image

`openapi-mocker serve` turns one contract file into a running mock, with no web
UI, no project to create and nothing left behind:

```bash
openapi-mocker serve spec.yaml            # OpenAPI (YAML/JSON), GraphQL, MCP or .proto
curl http://localhost:8080/users/1        # the operations live at the root
```

- **Flags:** `--port N` (default `$PORT`, else 8080), `--validation off|warn|enforce`
  (check requests against the contract), `--watch` (reload the file when it
  changes; a broken edit is reported and the previous spec keeps being
  served) and `--data-dir DIR` (keep state in `DIR`; by default a temporary
  directory is used and removed on exit). `GRPC_PORT` also serves a `.proto`
  contract over gRPC; the other environment settings (`LOG_MODE`,
  `LOG_SINKS`, `ADMIN_TOKEN`, ...) apply as for the full server.
- **Reserved paths:** `/healthz` and everything under `/__mocker`. There the
  management API is available with the project called `default`, so a test
  can assert what its service sent:
  `POST /__mocker/api/projects/default/verify?strict=true` (see [Test assertions](observability.md#test-assertions)), `GET /__mocker/metrics`, `GET /__mocker` (project id and links).
  A contract that defines `/healthz` itself is shadowed by the health check.
- **Exit status:** 0 after a clean stop (Ctrl+C or `SIGTERM`), 1 when serving
  fails (for example the port is taken), 2 for a usage error or a spec that
  cannot be read or is invalid.
- `openapi-mocker healthcheck [--url URL] [--timeout 3s]` asks a running server
  whether it is healthy (default `http://127.0.0.1:$PORT/healthz`); exit status
  0 means healthy. `openapi-mocker version` prints the build version.

### Container image

The image's `HEALTHCHECK` runs `openapi-mocker healthcheck` (no curl or wget
inside, and it follows `$PORT`; with `--port` pass `--url` yourself). It checks
every 10 s after a 3 s start period, so pipelines do not wait long. Images are
published to GitHub Container Registry for `linux/amd64` and `linux/arm64` by
`.github/workflows/docker-publish.yml`: `edge` follows `main`, a `vX.Y.Z` tag
publishes `X.Y.Z`, `X.Y` and `latest`. Every image is smoke-tested (starts,
turns healthy, serves a call, answers a verification) before it is pushed.

```bash
docker run -d --name mock -p 8080:8080 -v "$PWD/openapi.yaml:/spec/openapi.yaml:ro" \
  ghcr.io/eisenmann/openapi-mocker:latest serve /spec/openapi.yaml
docker inspect -f '{{.State.Health.Status}}' mock       # starting -> healthy
```

`docker compose up --wait` returns once the health check passes;
`deploy/docker-compose.serve.yml` is a ready-made file:
`SPEC=./openapi.yaml docker compose -f deploy/docker-compose.serve.yml up --wait`.

In CI, start the mock before the tests and stop it afterwards, for example in
GitHub Actions:

```yaml
- name: Start the mock
  run: |
    docker run -d --name mock -p 8080:8080 --health-interval 2s \
      -v "$PWD/openapi.yaml:/spec/openapi.yaml:ro" \
      ghcr.io/eisenmann/openapi-mocker:latest serve /spec/openapi.yaml
    until [ "$(docker inspect -f '{{.State.Health.Status}}' mock)" = healthy ]; do sleep 1; done
- run: ./run-tests.sh   # the service under test talks to http://localhost:8080
- name: Was the mock called as expected?
  run: |
    curl -fsS -X POST 'http://localhost:8080/__mocker/api/projects/default/verify?strict=true' \
      -H 'Content-Type: application/json' \
      -d '{"method":"POST","path":"/orders","body":{"json":{"sku":"A-1"}},"exactly":1}'
```

GitLab CI works the same way with the image as a `services:` entry that has
`command: ["serve", "/spec/openapi.yaml"]`, or with `docker run` in a
`docker:dind` job.
