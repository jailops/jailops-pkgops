# Releasing

Releases are cut straight on `master`: no pull request, no release branch.

## Cut a release

```sh
just release-dry v0.1.0   # all checks, changes and pushes nothing
just release v0.1.0
```

`just release` (`scripts/release.sh`) refuses unless: the tree is clean, the branch is `master` and not behind
`origin/master`, `vX.Y.Z[-pre]` is valid semver and the tag exists neither locally nor on origin, and
`CHANGELOG.md` has a non-empty `## [Unreleased]`. It then runs gofmt, `go vet`, `go test -race ./...`, the web
checks (`npm ci`, typecheck, tests, build, and that the committed `internal/webui/dist` is unchanged; skipped
with a warning when Node is missing), and builds a `git archive HEAD` copy with `CGO_ENABLED=0` to prove a clean
checkout needs no Node. Only then it writes `version.txt` (bare `0.1.0`, as in the other JailOps projects), moves
the Unreleased entries under `## [0.1.0] - DATE`, commits `Release v0.1.0`, creates the annotated tag and runs
`git push --atomic origin master refs/tags/v0.1.0`. If the web UI sources changed, run `just web` and commit
`internal/webui/dist` first. Write the Unreleased notes before releasing.

## What the workflow does

`.github/workflows/release.yml` runs on the `v*` tag with `contents: write` only (default `GITHUB_TOKEN`): gofmt,
vet and tests on the tagged commit, then GoReleaser (`.goreleaser.yaml`) builds `pkgops` for linux, darwin,
windows and freebsd (amd64, arm64; `CGO_ENABLED=0`, `-trimpath`, version/commit/date in ldflags), writes
`pkgops_X.Y.Z_OS_ARCH.tar.gz` (zip on windows, with LICENSE and README.md), `checksums.txt` (sha256), the changelog
from the commits, and creates the GitHub release (a `-rc` tag is marked prerelease).

## Verify a release

```sh
gh release view v0.1.0
v=0.1.0; gh release download v$v -p 'pkgops_*_linux_amd64.tar.gz' -p checksums.txt
sha256sum --ignore-missing -c checksums.txt && tar -xzf pkgops_${v}_linux_amd64.tar.gz && ./pkgops version
```

`pkgops version` must print the version, the commit and the build date. Local trial of the archives:
`just snapshot` (needs GoReleaser). `checksums.txt` protects against corruption, not against someone who can
replace the whole release; there are no signatures yet.

## Install the binary

- Download the archive (README, "Install").
- `go install jailops.net/pkgops/cmd/pkgops@v0.1.0` works only if `https://jailops.net/pkgops/...?go-get=1` answers
  with `<meta name="go-import" content="jailops.net/pkgops git https://github.com/jailops/jailops-pkgops">`.
  **UNVERIFIED, and not in place as of this writing**: the jailops-web site has that tag only for `jailops.net/jailops`
  (`src/pages/gomodule.html`); `/pkgops/` is the product page without the tag, and nginx serves subpaths such as
  `/pkgops/cmd/pkgops` only for `/jailops/`. To fix it, add `go_import: jailops.net/pkgops https://github.com/jailops/jailops-pkgops`
  to the front matter of `src/pages/pkgops.html` and a `location ^~ /pkgops/ { try_files /pkgops/index.html =404; }`
  in `nginx/jailops.net.conf` (as for `/jailops/`).
- `go install github.com/jailops/jailops-pkgops/cmd/pkgops@v0.1.0` does **not** work: `go.mod` declares
  `module jailops.net/pkgops`, and Go rejects a module fetched under a different path ("module declares its path
  as: jailops.net/pkgops but was required as: github.com/jailops/jailops-pkgops"). The alternative is a clone:
  `git clone --branch v0.1.0 https://github.com/jailops/jailops-pkgops && cd pkgops && go install ./cmd/pkgops`.
  `pkgops version` after a `go install ...@tag` takes the version from the module build info.

The action does not use `go install`: it downloads the release binary or builds its own checkout
(`scripts/install-pkgops.sh`).
