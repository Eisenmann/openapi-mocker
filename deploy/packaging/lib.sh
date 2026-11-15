#!/usr/bin/env bash
# Shared helpers for the package-manager renderers (sourced, not run).

# checksum_of FILE_NAME CHECKSUMS_FILE: the sha256 listed for a release asset.
# checksums.txt is the output of "sha256sum *": "<hash>  <file name>".
checksum_of() {
  local name="$1" sums="$2" sum
  sum="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$sums")"
  if ! [[ "$sum" =~ ^[0-9a-f]{64}$ ]]; then
    echo "error: no sha256 for $name in $sums" >&2
    return 1
  fi
  printf '%s' "$sum"
}

# asset_url REPO VERSION FILE_NAME: download URL of a release asset.
asset_url() {
  printf 'https://github.com/%s/releases/download/v%s/%s' "$1" "$2" "$3"
}

HOMEPAGE_URL="${HOMEPAGE_URL:-https://eisenmann.github.io/openapi-mocker/}"
DESCRIPTION="Mock server for OpenAPI, GraphQL, MCP and gRPC contracts"
