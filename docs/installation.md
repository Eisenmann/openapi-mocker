# Installation

Pick the way that fits your machine. Each one gives you the same
`openapi-mocker` command (or the same container image); check it with

```bash
openapi-mocker version
```

## Homebrew (macOS and Linux)

```bash
brew install Eisenmann/tap/openapi-mocker
```

This adds the `Eisenmann/homebrew-tap` tap on first use. Update with
`brew upgrade openapi-mocker`. The formula also installs the example contracts
under `$(brew --prefix)/share/openapi-mocker/examples`.

## Scoop (Windows)

```powershell
scoop bucket add eisenmann https://github.com/Eisenmann/scoop-bucket
scoop install openapi-mocker
```

Update with `scoop update openapi-mocker`.

## go install

With Go 1.26 or newer:

```bash
go install github.com/Eisenmann/openapi-mocker/cmd/openapi-mocker@latest
```

The binary is written to `$(go env GOPATH)/bin` (add that folder to your
`PATH`). Use `@v1.2.0` instead of `@latest` to pin a release. The web UI is
embedded in the binary, so nothing else is needed. `openapi-mocker version`
prints the module version; a build from a local checkout prints `dev`.

## Release archives

Every release has archives for Linux, macOS and Windows (amd64 and arm64) on the
[releases page](https://github.com/Eisenmann/openapi-mocker/releases), with a
`checksums.txt`:

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf openapi-mocker_1.2.0_linux_amd64.tar.gz
./openapi-mocker_1.2.0_linux_amd64/openapi-mocker version
```

Each archive holds the binary, the licence, the README and the examples. See
[Deployment and releases](deployment.md) for the archive names.

## Docker

Images for `linux/amd64` and `linux/arm64` are published to the GitHub
Container Registry:

```bash
docker run -p 8080:8080 -v mocker-data:/data ghcr.io/eisenmann/openapi-mocker:latest
```

| Tag | Meaning |
|---|---|
| `X.Y.Z` | one release, never changes |
| `X.Y` | the latest patch release of that minor version |
| `latest` | the latest release |
| `edge` | the latest build of the `main` branch |

Pin `X.Y.Z` (or `X.Y`) in CI. The [quick start](quickstart.md) shows how to
serve a contract file from the image and how to wait for it to become healthy.

## From source

```bash
git clone https://github.com/Eisenmann/openapi-mocker.git
cd openapi-mocker
go build -o openapi-mocker ./cmd/openapi-mocker
```
