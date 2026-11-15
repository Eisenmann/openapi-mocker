#!/usr/bin/env python3
"""Records the 30-second tour as an asciicast (v2) from a real run.

    go build -o /tmp/openapi-mocker ./cmd/openapi-mocker
    python3 deploy/demo/make-demo.py /tmp/openapi-mocker docs/assets/demo.cast

The commands are typed with a fixed rhythm, but every output is what the binary
and curl really printed in this run. Render the cast to the animated SVG that
the README shows with:

    npx svg-term-cli --in docs/assets/demo.cast --out docs/assets/demo.svg \
        --window --no-cursor --width 100 --height 30

Needs curl and a free port 8080. Run it again whenever the banner or the
Petstore example changes.
"""
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request

BIN, OUT = sys.argv[1], sys.argv[2]
PORT = "8080"
WIDTH, HEIGHT = 100, 30

events = []
clock = 0.0


def emit(text, delay=0.0):
    global clock
    clock += delay
    events.append([round(clock, 3), "o", text.replace("\n", "\r\n")])


def type_command(cmd):
    emit("\x1b[32m$\x1b[0m ", 0.35)
    for ch in cmd:
        emit(ch, 0.03)
    emit("\n", 0.25)


def show(output, pause):
    output = output.rstrip("\n")
    if output:
        emit(output + "\n", pause)


def run(shell_cmd, shown=None, pause=0.3, hold=1.0):
    type_command(shown or shell_cmd)
    done = subprocess.run(shell_cmd, shell=True, capture_output=True, text=True, timeout=30)
    show(done.stdout + done.stderr, pause)
    global clock
    clock += hold


env = {k: v for k, v in os.environ.items() if k != "PORT"}
log = tempfile.NamedTemporaryFile("w+", suffix=".log", delete=False)
server = subprocess.Popen([BIN, "serve", "--demo"], stderr=log, stdout=subprocess.DEVNULL, env=env)
try:
    for _ in range(100):
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{PORT}/healthz", timeout=1).read()
            break
        except Exception:
            time.sleep(0.1)
    else:
        sys.exit("the server did not become healthy")

    type_command("openapi-mocker serve --demo &")
    log.flush()
    show(open(log.name).read(), 0.6)
    emit("", 1.0)

    curl = f"curl -s -H 'Content-Type: application/json' localhost:{PORT}"
    run(f"curl -s localhost:{PORT}/pets", pause=0.35)
    run(f"{curl}/pets -d '{{\"name\":\"Bella\"}}'", pause=0.35)
    run(f"curl -s localhost:{PORT}/pets/4", pause=0.35)
    run(f"curl -s -X DELETE -w '%{{http_code}}\\n' localhost:{PORT}/pets/1", pause=0.3)
    run(f"curl -s -w '\\n%{{http_code}}\\n' localhost:{PORT}/pets/1", pause=0.3)
    run(
        f"{curl}/__mocker/api/projects/default/verify?strict=true "
        "-d '{\"method\":\"POST\",\"path\":\"/pets\",\"exactly\":1}' | grep -o '\"message\":\"[^\"]*\"'",
        shown=(
            "curl -s localhost:8080/__mocker/api/projects/default/verify?strict=true \\\n"
            "    -d '{\"method\":\"POST\",\"path\":\"/pets\",\"exactly\":1}' | grep -o '\"message\":\"[^\"]*\"'"
        ),
        pause=0.4,
        hold=2.0,
    )
finally:
    server.terminate()
    server.wait(timeout=15)

header = {"version": 2, "width": WIDTH, "height": HEIGHT, "title": "OpenAPI Mocker in 30 seconds",
          "env": {"SHELL": "/bin/bash", "TERM": "xterm-256color"}}
with open(OUT, "w", encoding="utf-8", newline="\n") as f:
    f.write(json.dumps(header) + "\n")
    for ev in events:
        f.write(json.dumps(ev, ensure_ascii=False) + "\n")
print(f"{OUT}: {len(events)} events, {clock:.1f} s")
