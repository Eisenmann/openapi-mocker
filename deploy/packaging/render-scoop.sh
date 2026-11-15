#!/usr/bin/env bash
# Prints the Scoop manifest for a release to stdout.
#
#   render-scoop.sh VERSION CHECKSUMS_FILE [OWNER/REPO]
#
# The release workflow commits the output to the bucket repository as
# bucket/openapi-mocker.json.
set -euo pipefail

version="${1:?usage: render-scoop.sh VERSION CHECKSUMS_FILE [OWNER/REPO]}"
sums="${2:?usage: render-scoop.sh VERSION CHECKSUMS_FILE [OWNER/REPO]}"
repo="${3:-${GITHUB_REPOSITORY:-Eisenmann/openapi-mocker}}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"

arch_block() { # arch_block ARCH
  local dir="openapi-mocker_${version}_windows_${1}"
  local file="${dir}.zip" sum
  sum="$(checksum_of "$file" "$sums")" || return 1 # a missing checksum must stop the render
  printf '    {"url": "%s", "hash": "%s", "extract_dir": "%s"}' \
    "$(asset_url "$repo" "$version" "$file")" "$sum" "$dir"
}

amd64="$(arch_block amd64)"
arm64="$(arch_block arm64)"

cat <<JSON
{
  "version": "${version}",
  "description": "${DESCRIPTION}",
  "homepage": "${HOMEPAGE_URL}",
  "license": "MIT",
  "architecture": {
    "64bit":
${amd64},
    "arm64":
${arm64}
  },
  "bin": "openapi-mocker.exe"
}
JSON
