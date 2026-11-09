#!/usr/bin/env bash
# Builds one release archive into ./dist:
#
#   deploy/release-build.sh OS ARCH VERSION     e.g. linux arm64 1.2.0
#
# OS is linux, darwin or windows, ARCH is amd64 or arm64. The archive is a
# .zip for Windows and a .tar.gz otherwise; it holds the binary, README,
# LICENSE and the examples. Run it from the repository root.
set -euo pipefail

os="${1:?usage: release-build.sh OS ARCH VERSION}"
arch="${2:?usage: release-build.sh OS ARCH VERSION}"
version="${3:?usage: release-build.sh OS ARCH VERSION}"

name="openapi-mocker_${version}_${os}_${arch}"
bin="openapi-mocker"
[ "$os" = "windows" ] && bin="openapi-mocker.exe"

rm -rf "dist/$name"
mkdir -p "dist/$name"

CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
  go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "dist/$name/$bin" ./cmd/server

cp README.md LICENSE "dist/$name/"
cp -r examples "dist/$name/examples"

(
  cd dist
  if [ "$os" = "windows" ]; then
    zip -qr "$name.zip" "$name"
  else
    tar -czf "$name.tar.gz" "$name"
  fi
  rm -rf "$name"
)

echo "built dist/$name"
