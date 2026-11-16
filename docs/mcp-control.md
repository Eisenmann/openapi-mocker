# MCP control plane

`openapi-mocker mcp` lets an AI agent in your editor or CI run the mock server
itself. The agent creates a project from a contract, shapes the mock's
behaviour, checks what the system under test sent, and runs a load test. No web
UI and no REST calls are needed.

It speaks the [MCP](https://modelcontextprotocol.io) stdio transport, so any MCP
client can use it.

## Connect an agent

Add the server to the client's MCP configuration:

```json
{
  "mcpServers": {
    "openapi-mocker": { "command": "openapi-mocker", "args": ["mcp"] }
  }
}
```

The command also starts a mock server on a free port of the local machine
(`127.0.0.1`). Every project is served at
`http://127.0.0.1:PORT/mock/PROJECT_ID`, and the tools report that address as
`mock_url`. The web UI is available at `http://127.0.0.1:PORT`.

| Flag | Meaning |
| --- | --- |
| `--port N` | Fixed port for the mock server. Default: a free port. |
| `--data-dir DIR` | Keep projects in `DIR` between runs. Default: a temporary directory that is removed on exit. |

Standard output carries protocol messages only; diagnostics go to standard
error. The server stops when the client closes its input, on Ctrl+C or on
SIGTERM.

## A session

1. `create_project` with an OpenAPI, GraphQL, MCP or `.proto` contract. The
   endpoints answer at once with examples from the contract.
2. `add_mock_rule` for a fixed response, `set_fault` for latency, random
   errors, timeouts, dropped connections or malformed bodies.
3. The system under test calls `mock_url`.
4. `verify_calls` asserts what it sent: method, path, query, headers, body and
   how many times. When it fails, the result lists the closest calls and what
   differed. `get_logs` shows the calls themselves.
5. `run_load_test` generates a plan from the contract (or takes your own) and
   returns the verdict and latency figures.

A project can be named by its id or its name. Leave it out when there is only
one project.

## Tools

| Tool | What it does |
| --- | --- |
| `list_projects`, `create_project`, `update_project`, `delete_project` | Manage projects; `update_project` sets validation mode and stateful mode. |
| `validate_contract`, `import_contract`, `get_contract`, `list_endpoints` | Check, save and read contracts. A new version is served at once; invalid contracts are rejected with the reasons. |
| `list_mock_rules`, `add_mock_rule`, `set_fault`, `delete_mock_rule` | Fixed responses, request matching, delay, failure rate and faults. |
| `get_logs`, `verify_calls`, `clear_calls` | Read the request log, assert on it, reset it. |
| `run_load_test`, `get_load_run`, `abort_load_run` | Start, poll and stop load runs. `wait_seconds` waits up to 120 seconds for the result. |

Arguments use `snake_case` and unknown arguments are rejected, so a typo is
reported instead of ignored. A failing tool returns an error result that says
what to fix; the session continues.

## Notes

- Delete and clear tools are marked as destructive, so clients can ask before
  running them.
- Load plans generated from a contract call `GET` operations only. Pass
  `include_unsafe` to include operations that change data; the target is always
  the local mock.
- Without `--data-dir`, everything is gone when the server stops. Use
  `--data-dir` to keep a project across sessions.
