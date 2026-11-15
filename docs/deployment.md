# Deployment and releases

Docker and the headless `serve` mode are covered in the [quick start](quickstart.md).

## Releases

Pushing a tag `vX.Y.Z` (or creating a release with a new tag in GitHub) runs
`.github/workflows/release.yml`, which tests the code, builds the binaries and
attaches them to the GitHub release together with `checksums.txt`:

| OS | Architectures | Archive |
| --- | --- | --- |
| Linux | amd64, arm64 | `openapi-mocker_X.Y.Z_linux_<arch>.tar.gz` |
| macOS | amd64 (Intel), arm64 (Apple silicon) | `openapi-mocker_X.Y.Z_darwin_<arch>.tar.gz` |
| Windows | amd64, arm64 | `openapi-mocker_X.Y.Z_windows_<arch>.zip` |

Each archive holds the binary (`openapi-mocker version` prints `X.Y.Z`), the
README, the licence and the examples. The container image is published on the
same tag by `docker-publish.yml` (`ghcr.io/eisenmann/openapi-mocker:X.Y.Z`, `X.Y`
and `latest`). A release that already exists (for example one you wrote notes
for in the GitHub UI) only gets the files added; otherwise one is created with
the notes from `docs/release-notes/vX.Y.Z.md` when that file exists, or generated
ones. A tag with a suffix such as `v1.3.0-rc.1` makes a pre-release. To build
an archive locally: `bash deploy/release-build.sh linux amd64 1.2.0`.

## Kubernetes

```bash
kubectl apply -f deploy/k8s/00-namespace.yaml
kubectl apply -f deploy/k8s/
# (the image must be reachable by the cluster: docker build + push to your
#  registry, then fix image: in deploy/k8s/03-deployment.yaml)
kubectl -n openapi-mocker port-forward svc/openapi-mocker 8080:80
```
