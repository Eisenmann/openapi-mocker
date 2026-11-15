# Releasing (maintainers)

A release is a tag. Everything else runs from the workflows in
`.github/workflows/` (the sources are in `deploy/workflows-to-copy/`).

## Cutting a release

```bash
git tag v1.2.0
git push origin v1.2.0
```

Creating a release with a new tag in the GitHub UI does the same. The tag
starts two workflows:

| Workflow | Result |
|---|---|
| `release.yml` | tests, then archives for six platforms and `checksums.txt` on the GitHub release; then the Homebrew tap and Scoop bucket are updated |
| `docker-publish.yml` | the multi-arch image `ghcr.io/<owner>/openapi-mocker` with the tags `1.2.0`, `1.2` and `latest`, published only after a smoke test passes |

A tag with a suffix (`v1.3.0-rc.1`) makes a pre-release: it gets archives and
an image, but Homebrew and Scoop are not updated.

Release notes come from `docs/release-notes/vX.Y.Z.md` when that file exists,
else they are generated from the merged pull requests.

## One-time setup

1. **Copy the workflows.** Copy `ci.yml`, `release.yml`, `docker-publish.yml` and
   `docs.yml` from `deploy/workflows-to-copy/` to `.github/workflows/`.
2. **Make the image public.** The first published image is private. Open the
   package under the repository's *Packages*, then *Package settings*, then
   *Change visibility*, and choose *Public*. Later tags keep that setting.
3. **Create the tap and the bucket.** Two empty public repositories in the same
   account: `homebrew-tap` (the `homebrew-` prefix is required by Homebrew) and
   `scoop-bucket`. Use other names by setting `TAP_REPO` and `BUCKET_REPO` in
   the `packages` job.
4. **Add a token.** The workflow's own token cannot push to other repositories.
   Create a fine-grained personal access token that has *Contents: Read and
   write* on those two repositories only, and store it as the repository secret
   `PACKAGES_TOKEN`. Without the secret the `packages` job is skipped with a
   notice, and the rest of the release is not affected.

## What the package files look like

`deploy/packaging/render-homebrew.sh` and `render-scoop.sh` turn the version and
`checksums.txt` into `Formula/openapi-mocker.rb` and `bucket/openapi-mocker.json`;
`publish.sh` commits them to the two repositories. `deploy/packaging/test.sh`
checks the renderers without network access and runs in CI. To see the output
for a release you already have:

```bash
gh release download v1.2.0 --pattern checksums.txt
bash deploy/packaging/render-homebrew.sh 1.2.0 checksums.txt
bash deploy/packaging/render-scoop.sh 1.2.0 checksums.txt
```
