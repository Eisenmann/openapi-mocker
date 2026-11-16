# Privacy and the usage ping

OpenAPI Mocker can send an anonymous usage ping. It is **off by default**. It
only runs after you turn it on, and you can see exactly what it sends before
you do.

The reason it exists: the project is built by a small team, and the ping is the
only way to learn which protocols and installs are really used. It decides what
gets built next.

## Turn it on or off

```bash
openapi-mocker telemetry status    # is it on, and why
openapi-mocker telemetry show      # print exactly what a ping contains
openapi-mocker telemetry enable    # turn it on
openapi-mocker telemetry disable   # turn it off and delete the install id
```

The choice is saved in `telemetry.json` in your user configuration directory
(for example `~/.config/openapi-mocker` on Linux). Set
`OPENAPI_MOCKER_CONFIG_DIR` to keep it elsewhere.

In a container there is usually no configuration directory. Set
`OPENAPI_MOCKER_TELEMETRY=1` to turn the ping on for that environment, or `0`
to turn it off. Without a persistent directory the install id lasts only as
long as the process, so a restarted container counts as a new install.

`DO_NOT_TRACK=1` always turns the ping off, whatever else is set. The
environment variable `OPENAPI_MOCKER_TELEMETRY` wins over the saved choice.

When you start `serve` or the server in a terminal and have not chosen, one
line tells you that the ping exists. It is not shown in CI or when output is
piped.

## What a ping contains

A ping is one JSON document, and these are all its fields:

```json
{
  "schema": 1,
  "install_id": "15363fcdbbf3fdb62b84e96555e2ff4e",
  "version": "1.4.0",
  "os": "linux",
  "arch": "amd64",
  "mode": "serve",
  "protocols": ["openapi"]
}
```

| Field | Meaning |
|---|---|
| `schema` | The version of this layout. |
| `install_id` | A random 128-bit value made when you turn the ping on. It is not derived from your machine, user name or network. `telemetry disable` deletes it, and turning the ping on again makes a new one. |
| `version` | The OpenAPI Mocker version. |
| `os`, `arch` | The operating system and processor architecture, for example `linux` and `arm64`. |
| `mode` | `serve` (the `serve` command) or `server` (the full server). |
| `protocols` | The kinds of contract being served: `openapi`, `graphql`, `mcp` or `grpc`. |

A ping is sent when the process starts and then once every 24 hours while it
keeps running. Nothing is sent by `diff`, `load`, `healthcheck` or the other
commands.

## What is never sent

- Contract content, operation names, paths, URLs or example data.
- Requests, responses, headers, tokens or logs.
- Host names, user names, file paths or environment variables.
- Anything about the contracts' content beyond the four kind names above.

## The collector

A ping is an HTTPS request, so the server that receives it can see the IP
address of the connection, as with any request on the internet. The project's
collector discards the address and stores only the fields above. A collector
must not store addresses.

A build only sends pings to the collector built into it; a build without one
cannot send anything, even when you turn the ping on (`telemetry status` says
so). `OPENAPI_MOCKER_TELEMETRY_URL` replaces the collector, for example to send
pings to your own. The address must be `https`, or `http` for the local machine
only. Redirects are not followed, and a failed ping is logged and ignored: it
never delays or stops the server.

To run your own collector, accept `POST` with the JSON above and answer with any
`2xx` status.

## Verify it

The code is in `internal/telemetry` and `cmd/openapi-mocker/telemetry.go`. A
test checks that a ping has exactly the seven fields listed above.
