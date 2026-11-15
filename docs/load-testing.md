# Load testing

openapi-mocker can load test the mock it serves, or any HTTP API you are
allowed to load, from a plan file. The plan is the same whether you run it
from the command line, in CI or through the REST API. It generates the load
with an HDR histogram per request, measures latency from the time a request
was *scheduled* (so a stalled server shows up as slow requests instead of
being hidden), and checks the result against thresholds.

```bash
# a starter plan from a contract (every GET/HEAD operation, a ramp, sane thresholds)
openapi-mocker load generate petstore.yaml --target https://staging.example.com -o plan.yaml

# run it; the exit status is 0 (all thresholds passed), 1 (a threshold failed or the
# run was aborted) or 2 (usage error or an invalid plan)
openapi-mocker load run plan.yaml --contract petstore.yaml --out report --format html,json,junit

openapi-mocker load validate plan.yaml   # check a plan without sending anything
```

`examples/load-plan.yaml` (operations taken from the contract, contract checks,
an aborting threshold) and `examples/load-plan-steps.yaml` (hand-written steps,
a data file, value extraction, think time) show the format. A plan is YAML or
JSON:

| Key | Meaning |
|---|---|
| `target` | `baseUrl` (`mock://<projectId>` for this server's own mock, or an `http(s)` URL), `allowHosts`, `http` (protocol, timeouts, connection limits), `auth` (`bearer`, `basic` or `header`; `ref` names a secret, never the secret itself) |
| `data` | named CSV/JSON files (`mode`: `sequential`, `random` or `unique`), used as `{{data.users.email}}` |
| `scenarios` | each with a `weight` and `steps`: an `operation` from the contract (`listPets` or `GET /pets/{id}`), or a hand-written `method`, `path`, `headers`, `body`; `checks`, `extract` and `think` |
| `load` | `model`: `open` (an arrival rate; the default choice for latency), `closed` (virtual users) or `iterations`; `stages`, `warmup`, `gracefulStop`, `maxInFlight`, `contractSampleEvery` |
| `thresholds` | `http.latency.p95 < 500ms`, `errors.rate < 1%`, `contract.violations == 0`, `throughput.rps > 20`, scoped with `{operation=listPets}`; `abortOnFail` ends the run early |
| `outputs` | report formats: `html`, `json`, `junit`, `csv` |

Checks: `status in [200,201]`, `latency < 800ms`, `$.id exists`,
`$.name == "rex"`, `$.tags contains "a"` and `contract` (the response is
validated against the operation's response schema; set `contractSampleEvery`
to check one response in N under heavy load).

**Reports.** The HTML report is one self-contained file (no scripts or fonts
fetched from anywhere): verdict, thresholds, percentiles over time, a latency
histogram and heat map, errors, per-operation and per-step breakdowns,
connection-phase timings, the load generator's own health and the slowest and
first failing requests with redacted headers. The JSON report follows
[`docs/load-report.schema.json`](https://github.com/Eisenmann/openapi-mocker/blob/main/docs/load-report.schema.json); JUnit XML maps each threshold to a test case.
When the generator could not keep to the plan (it was too slow to start the
requests on time) the report says so and marks its figures as untrustworthy
instead of presenting them as fact.

**Safety.** Load is only sent where it is allowed:

- A `mock://` target is this server's own mock of the project the run belongs to.
- An external target needs its host in the plan's `allowHosts` (`*.example.com`
  matches subdomains), and a caller with the `admin` or `load-runner` role. The
  command line allows the host you give with `--target`.
- The server caps what a plan may ask for: `LOAD_MAX_RPS` (1000),
  `LOAD_MAX_VUS` (1000), `LOAD_MAX_DURATION` (`1h`) and `LOAD_MAX_CONNS` (1000);
  `0` switches a cap off. `load run` applies no caps.
- `LOAD_ALLOW_HOSTS` (comma-separated, the patterns of `allowHosts`) limits the
  external hosts of the whole server; a plan's `allowHosts` can only narrow it.
  Without it, the server refuses private, loopback, link-local and shared
  (CGNAT) addresses, such as `localhost`, `10.x.x.x` or the cloud metadata
  address. The check also runs on the address a name resolves to when the
  connection is made, so a DNS name that points inside is refused too.
  `LOAD_ALLOW_PRIVATE=true` switches the ban off. `load run` is not restricted.
- One run per project at a time; a run can be aborted, and active runs are
  stopped (and saved) when the server shuts down.
- On the server, `target.auth.ref` reads the environment variable
  `LOAD_SECRET_<ref>`, so a plan can never name `ADMIN_TOKEN` or another setting.
  Data files are read from `DATA_DIR/loaddata/`. The command line reads secrets
  from the environment variable named by `ref` and data files next to the plan.
- Secrets, `Authorization`, cookies and API keys are redacted in stored samples.

**Storage.** Plans are stored per project and versioned like contracts
(`DATA_DIR/loadplans/`); runs and their detail files live in `DATA_DIR/loadruns/`.
`LOAD_RUNS_KEEP` (default 50 per project) and `LOAD_RUNS_MAX_AGE` (default
`30d`) set the retention; active runs are never removed, and a run that was
active when the server stopped is marked failed on the next start.

**Roles.** The new `load-runner` role sits between `viewer` and `editor`: it
may read the project, save plans and run, abort and delete runs, also against
allowed external targets. An `editor` may run against the project's own mock
only. Reports quote requests and responses of the target, so reading one needs
`load-runner`; listing plans and runs is open to a `viewer`. Plan saves and run
starts, aborts and deletes are audited (`load.plan.save`, `load.run.start`, ...).

**Accuracy.** `internal/loadtest/accuracy_test.go` runs the engine against a
server with known latency (a fixed delay, a lognormal one, and a one-second
stall) and compares the reported percentiles and request count with what the
server itself measured; `go test -short` skips it. Comparing with another tool
on the same target (for example k6 with a `constant-arrival-rate` executor) is a
good sanity check for your own environment: expect the same request count and
percentiles within a few milliseconds, and differences in the tail where a tool
that waits for each answer before sending the next one hides stalls.
