#!/usr/bin/env bash
# Installs the pinned versions of the tools compared in docs/benchmark.md into
# benchmark/.tools and runs the benchmark. Needs Go, Node.js (npm), Java 17+
# and Python 3. Usage: benchmark/run-all.sh [extra run.py arguments]
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tools="$root/benchmark/.tools"
results="$root/benchmark/results"

mkdir -p "$tools" "$results"

echo "== building openapi-mocker"
(cd "$root" && go build -o "$tools/openapi-mocker" ./cmd/openapi-mocker)

echo "== installing hey v0.1.4"
GOBIN="$tools" go install github.com/rakyll/hey@v0.1.4

echo "== installing WireMock 3.13.2, Prism 5.16.0 and Mockoon CLI 9.10.0"
(cd "$tools" && npm install --no-audit --no-fund --prefix . \
  wiremock@3.13.2 @stoplight/prism-cli@5.16.0 @mockoon/cli@9.10.0)

jar="$(find "$tools/node_modules/wiremock" -name 'wiremock-standalone-*.jar' | head -n 1)"
out="$results/$(date -u +%Y%m%d-%H%M%S).json"

echo "== running the benchmark (about 10 minutes)"
python3 "$root/benchmark/run.py" \
  --out "$out" \
  --hey "$tools/hey" --hey-version v0.1.4 \
  --openapi-mocker "$tools/openapi-mocker" \
  --wiremock-jar "$jar" \
  --prism "$tools/node_modules/.bin/prism" \
  --mockoon "$tools/node_modules/.bin/mockoon-cli" \
  "$@"

echo "results written to $out"
