# Benchmark

This page compares OpenAPI Mocker with three other mock servers that can serve
an API contract or a set of stubs: WireMock, Prism and Mockoon CLI. The
harness, the stubs and the raw results are in the repository
(`benchmark/`), so you can repeat every number on your own machine.

## Results

Reference run, 2026-11-16. Every tool served the same Petstore and answered
`GET /pets/1`. Lower is better for start time, memory and latency; higher is
better for requests per second.

| Tool | Version | Start to first answer | Idle memory | Requests/s (1 connection) | Requests/s (32 connections) | p99 at 32 connections | Peak memory under load |
|---|---|---|---|---|---|---|---|
| OpenAPI Mocker | built from source | 13 ms | 16 MiB | 5,169 | 12,499 | 20 ms | 48 MiB |
| WireMock | 3.13.2 | 1,174 ms | 99 MiB | 4,123 | 7,077 | 33 ms | 1,082 MiB |
| Prism | 5.16.0 | 610 ms | 150 MiB | 1,253 | 2,285 | 28 ms | 302 MiB |
| Mockoon CLI | 9.10.0 | 575 ms | 117 MiB | 251 | 903 | 67 ms | 311 MiB |

Start time is the median of 10 starts. Throughput is the median of 3 runs of
10 seconds each, after a 5 second warm-up. No request failed in any run. The
raw numbers, including every single run, are in
`benchmark/results/2026-11-16-reference.json`.

## What was measured

- **Start to first answer**: from spawning the process to the first `200`
  response, polled every 5 ms.
- **Memory**: resident set size (RSS) of the whole process tree, read from
  `/proc`, so a JVM or a Node.js child process is counted. Idle memory is taken
  after the first answer; peak memory is the highest value sampled during load.
- **Requests per second and latency**: the [hey](https://github.com/rakyll/hey)
  load generator against `GET /pets/1`, at 1 and 32 concurrent connections.

## How each tool was run

All tools run with their default settings and stay on the same machine as the
load generator.

| Tool | Command | Contract |
|---|---|---|
| OpenAPI Mocker | `openapi-mocker serve examples/petstore.yaml` | the OpenAPI file |
| WireMock | `java -jar wiremock-standalone.jar --root-dir benchmark/wiremock` | hand-written stubs |
| Prism | `prism mock examples/petstore.yaml` | the OpenAPI file |
| Mockoon CLI | `mockoon-cli start --data examples/petstore.yaml` | the OpenAPI file (imported) |

WireMock has no OpenAPI mode, so it serves two hand-written stubs
(`benchmark/wiremock/mappings/petstore.json`) with the same response bodies.
That is the best case for WireMock: it does no contract work at all.

## Read these before you quote the numbers

- **One small machine.** The run used a shared 2 vCPU virtual machine with 8 GiB
  of memory, and the load generator competed with the server for the same two
  cores. Absolute numbers will be higher on a bigger machine. Compare the tools
  with each other, not with other benchmarks.
- **One simple request.** `GET /pets/1` returns a small fixed document. It says
  nothing about large responses, templating, request validation, proxying or
  stateful mocks.
- **Different work per request.** The responses are the same in content but not
  in size (the stubs and the tools format JSON differently), and each tool
  validates and logs differently by default. OpenAPI Mocker records every request
  in its request log, which costs time that a tool without one does not pay.
- **Defaults only.** Nobody tuned the JVM, Node.js or any server setting.
  Memory of the JVM and Node.js tools depends strongly on those defaults and on
  the machine, so treat their peak memory as indicative only.
- **Start time is process start.** For WireMock and the Node.js tools, it
  includes the runtime start-up.

## What the benchmark found in OpenAPI Mocker

The first runs of this benchmark showed OpenAPI Mocker at about 150 requests per
second, far behind the others. Profiling found two costs paid on every request:

1. the contract was parsed again for each request, which took about 60% of the
   time;
2. every request-log entry rewrote the whole data file.

Both are fixed in the same change as this page. The contract is now parsed once
per version and kept (a small cache keyed by the contract's content), and request
logs are written to disk in batches every 200 ms and when the server stops. The
cost of one request in the Go benchmark went from 5.9 ms to 35 µs.

One consequence: if the process is killed, the last 200 ms of request logs may be
lost. Every other change is still written immediately.

## Run it yourself

You need Go, Node.js with npm, Java 17 or newer and Python 3.

```bash
benchmark/run-all.sh
```

The script builds OpenAPI Mocker, installs the pinned versions of the other
tools into `benchmark/.tools`, runs the benchmark (about 10 minutes) and writes
a JSON file to `benchmark/results/`. Pass extra options to `benchmark/run.py`
after the script name, for example `--only openapi-mocker` or
`--concurrency 1,8,64`.
