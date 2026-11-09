#!/usr/bin/env bash
# Smoke test of the container image: serve a contract, wait for the health
# check, call an operation and assert the call through the management API.
#
#   deploy/smoke-test.sh IMAGE
set -euo pipefail

image="${1:?usage: smoke-test.sh IMAGE}"
here="$(cd "$(dirname "$0")" && pwd)"
name="openapi-mocker-smoke-$$"

cleanup() {
  docker logs "$name" 2>&1 | tail -n 20 || true
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$name" -p 127.0.0.1::8080 \
  --health-interval 2s --health-start-period 2s \
  -v "$here/../examples:/specs:ro" \
  "$image" serve /specs/simple-contract.yaml >/dev/null

for _ in $(seq 1 30); do
  status="$(docker inspect -f '{{.State.Health.Status}}' "$name")"
  [ "$status" = "healthy" ] && break
  [ "$status" = "unhealthy" ] && { echo "container is unhealthy" >&2; exit 1; }
  sleep 2
done
[ "$status" = "healthy" ] || { echo "container did not become healthy" >&2; exit 1; }

base="http://$(docker port "$name" 8080/tcp | head -n 1)"

curl -fsS "$base/ping" | grep -q message
curl -fsS -X POST "$base/__mocker/api/projects/default/verify?strict=true" \
  -H 'Content-Type: application/json' \
  -d '{"method":"GET","path":"/ping","exactly":1}' >/dev/null

echo "smoke test passed: $image"
