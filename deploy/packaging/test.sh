#!/usr/bin/env bash
# Checks the package-manager renderers with made-up checksums. Needs no network.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

version=1.2.3
sums="$tmp/checksums.txt"
i=0
for f in linux_amd64.tar.gz linux_arm64.tar.gz darwin_amd64.tar.gz darwin_arm64.tar.gz \
  windows_amd64.zip windows_arm64.zip; do
  i=$((i + 1))
  printf '%064x  openapi-mocker_%s_%s\n' "$i" "$version" "$f" >> "$sums"
done

fail() { echo "FAIL: $*" >&2; exit 1; }

formula="$tmp/openapi-mocker.rb"
bash "$here/render-homebrew.sh" "$version" "$sums" Eisenmann/openapi-mocker > "$formula"
manifest="$tmp/openapi-mocker.json"
bash "$here/render-scoop.sh" "$version" "$sums" Eisenmann/openapi-mocker > "$manifest"

# Every asset must be referenced with its own checksum.
n=0
for f in linux_amd64.tar.gz linux_arm64.tar.gz darwin_amd64.tar.gz darwin_arm64.tar.gz; do
  n=$((n + 1)) # the order above is the order of the checksum lines
  want="$(awk -v f="openapi-mocker_${version}_$f" '$2 == f { print $1 }' "$sums")"
  url="https://github.com/Eisenmann/openapi-mocker/releases/download/v$version/openapi-mocker_${version}_$f"
  grep -qF "url \"$url\"" "$formula" || fail "formula lacks $url"
  grep -A1 -F "url \"$url\"" "$formula" | grep -qF "sha256 \"$want\"" || fail "formula checksum for $f"
done
for f in windows_amd64.zip windows_arm64.zip; do
  want="$(awk -v f="openapi-mocker_${version}_$f" '$2 == f { print $1 }' "$sums")"
  grep -qF "\"hash\": \"$want\"" "$manifest" || fail "manifest checksum for $f"
  grep -qF "openapi-mocker_${version}_${f%.zip}\"" "$manifest" || fail "manifest extract_dir for $f"
done
grep -qF 'version "1.2.3"' "$formula" || fail "formula version"

if command -v python3 >/dev/null; then
  python3 -I -c 'import json,sys; m=json.load(open(sys.argv[1])); assert m["version"]=="1.2.3"; assert m["bin"]=="openapi-mocker.exe"; assert set(m["architecture"])=={"64bit","arm64"}' "$manifest" \
    || fail "manifest is not the expected JSON"
fi
if command -v ruby >/dev/null; then
  ruby -c "$formula" >/dev/null || fail "formula is not valid Ruby"
fi

# A missing checksum must stop the render instead of writing an empty hash.
grep -v windows_arm64 "$sums" > "$tmp/short.txt"
if bash "$here/render-scoop.sh" "$version" "$tmp/short.txt" >/dev/null 2>&1; then
  fail "render-scoop.sh accepted a checksum file without windows_arm64"
fi

echo "packaging templates OK"
