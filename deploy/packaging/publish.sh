#!/usr/bin/env bash
# Updates the Homebrew tap and the Scoop bucket for a release.
#
#   PACKAGES_TOKEN=... publish.sh VERSION CHECKSUMS_FILE
#
# PACKAGES_TOKEN needs write access to the contents of both repositories (the
# workflow's own GITHUB_TOKEN cannot push to other repositories). The
# repositories default to <owner>/homebrew-tap and <owner>/scoop-bucket and can
# be changed with TAP_REPO and BUCKET_REPO.
set -euo pipefail

version="${1:?usage: publish.sh VERSION CHECKSUMS_FILE}"
sums="$(cd "$(dirname "${2:?usage: publish.sh VERSION CHECKSUMS_FILE}")" && pwd)/$(basename "$2")"
: "${PACKAGES_TOKEN:?PACKAGES_TOKEN is not set}"

owner="${GITHUB_REPOSITORY_OWNER:-Eisenmann}"
tap_repo="${TAP_REPO:-$owner/homebrew-tap}"
bucket_repo="${BUCKET_REPO:-$owner/scoop-bucket}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# push_file REPO RENDERER DEST: render into a clone of REPO and push when changed.
push_file() {
  local repo="$1" renderer="$2" dest="$3" dir="$work/${1//\//_}"

  git clone --depth 1 "https://x-access-token:${PACKAGES_TOKEN}@github.com/${repo}.git" "$dir"
  mkdir -p "$dir/$(dirname "$dest")"
  bash "$here/$renderer" "$version" "$sums" > "$dir/$dest"

  git -C "$dir" add "$dest"
  if git -C "$dir" diff --cached --quiet; then
    echo "$repo: $dest is already up to date"
    return
  fi

  git -C "$dir" -c user.name="github-actions[bot]" \
    -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
    commit -m "openapi-mocker ${version}"
  git -C "$dir" push origin HEAD
  echo "$repo: updated $dest to $version"
}

push_file "$tap_repo" render-homebrew.sh Formula/openapi-mocker.rb
push_file "$bucket_repo" render-scoop.sh bucket/openapi-mocker.json
