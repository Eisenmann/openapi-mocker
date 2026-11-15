#!/usr/bin/env bash
# Prints the Homebrew formula for a release to stdout.
#
#   render-homebrew.sh VERSION CHECKSUMS_FILE [OWNER/REPO]
#
# VERSION is without the leading "v" (1.2.0). CHECKSUMS_FILE is the
# checksums.txt of the release. The release workflow commits the output to the
# tap repository as Formula/openapi-mocker.rb.
set -euo pipefail

version="${1:?usage: render-homebrew.sh VERSION CHECKSUMS_FILE [OWNER/REPO]}"
sums="${2:?usage: render-homebrew.sh VERSION CHECKSUMS_FILE [OWNER/REPO]}"
repo="${3:-${GITHUB_REPOSITORY:-Eisenmann/openapi-mocker}}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"

block() { # block OS ARCH
  local file="openapi-mocker_${version}_${1}_${2}.tar.gz" sum
  sum="$(checksum_of "$file" "$sums")" || return 1 # a missing checksum must stop the render
  printf '      url "%s"\n      sha256 "%s"\n' "$(asset_url "$repo" "$version" "$file")" "$sum"
}

darwin_arm64="$(block darwin arm64)"
darwin_amd64="$(block darwin amd64)"
linux_arm64="$(block linux arm64)"
linux_amd64="$(block linux amd64)"

cat <<RUBY
class OpenapiMocker < Formula
  desc "${DESCRIPTION}"
  homepage "${HOMEPAGE_URL}"
  version "${version}"
  license "MIT"

  on_macos do
    on_arm do
${darwin_arm64}
    end
    on_intel do
${darwin_amd64}
    end
  end

  on_linux do
    on_arm do
${linux_arm64}
    end
    on_intel do
${linux_amd64}
    end
  end

  def install
    bin.install "openapi-mocker"
    pkgshare.install "examples"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/openapi-mocker version")
  end
end
RUBY
