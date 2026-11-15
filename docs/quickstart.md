# Quick start

## The 30-second tour

![A terminal session: openapi-mocker serve --demo, then curl calls that list, add, fetch and remove pets, and a verification of the calls](assets/demo.svg)

[Install](installation.md) the binary, or use the Docker image, and start the
built-in Petstore. It needs no file, shows no UI and stops with Ctrl+C:

```bash
openapi-mocker serve --demo
# or: docker run --rm -p 8080:8080 ghcr.io/eisenmann/openapi-mocker:latest serve --demo
```

In another terminal, list the three pets, add one and fetch it, then remove
another. The mock is stateful, so later calls see the earlier ones:

```bash
curl localhost:8080/pets
curl -H 'Content-Type: application/json' localhost:8080/pets -d '{"name":"Bella"}'
curl localhost:8080/pets/4
curl -X DELETE -i localhost:8080/pets/1      # 204, and GET /pets/1 is now a 404
```

Ask the mock what your code sent it, which is how a test checks its calls. A
failed check answers `409`, so `curl --fail` fails the test:

```bash
curl --fail -H 'Content-Type: application/json' \
  'localhost:8080/__mocker/api/projects/default/verify?strict=true' \
  -d '{"method":"POST","path":"/pets","exactly":1}'
```

The recording above is made by `deploy/demo/make-demo.py` from a real run; its
text version is [`assets/demo.cast`](assets/demo.cast).

## Serve your own contract

Give `serve` any OpenAPI (YAML or JSON), GraphQL, MCP or `.proto` file. The
Petstore is also a file, so you can open it, change it and serve it:

```bash
curl -O https://raw.githubusercontent.com/Eisenmann/openapi-mocker/main/examples/petstore.yaml
openapi-mocker serve petstore.yaml --watch     # reloads when you save the file
curl localhost:8080/pets                       # example data from the contract
```

Without `--stateful` every call is answered from the contract, as Prism and
most mock servers do: a created pet is not remembered. Add `--stateful` and
`POST`, `PUT`, `PATCH` and `DELETE` change what later `GET`s return (the
collections start empty). The other flags, the reserved paths and the exit
codes are listed under [Sandbox](#sandbox-serve-and-the-ci-image) below.

## Desktop with the web UI

Install the binary with Homebrew, Scoop, `go install` or a release archive
(see [Installation](installation.md)), then run it:

```bash
openapi-mocker
# UI:  http://localhost:8080
```

From a source checkout:

```bash
go run ./cmd/openapi-mocker
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
  (check requests against the contract), `--stateful` (`POST`, `PUT`, `PATCH`
  and `DELETE` change what later `GET`s return; kept in memory, or saved in
  `DIR` when `--data-dir` is given), `--demo` (serve the built-in Petstore,
  stateful and with three pets, instead of a file), `--watch` (reload the file
  when it changes; a broken edit is reported and the previous spec keeps being
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
