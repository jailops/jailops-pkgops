# Releasing

Releases are cut straight on `master`: no pull request, no release branch.

## Cut a release

The flow is PR-based, as in the other JailOps repositories:

```sh
just release-pr 0.2.0     # branch release/v0.2.0: version.txt and the CHANGELOG.md section, and a pull request
# review, CI, merge the PR
just release 0.2.0        # master carries 0.2.0: signs the tag v0.2.0 and pushes it
```

`release-pr` takes a bare `X.Y.Z` (it adds the `v`), refuses a dirty tree, an empty `## Unreleased` section in
`CHANGELOG.md`, and a version that master already has. It moves the Unreleased entries under `## X.Y.Z - DATE` (keeping
an empty Unreleased on top) and opens the PR with `gh`. `release` checks out and fast-forwards master, requires
`version.txt` and the CHANGELOG section to say the version, refuses an existing tag, signs the tag (`git tag -s`, so a
signing key must be configured) and pushes it. It runs no tests: the tag starts the release workflow, which checks the
tagged commit before GoReleaser publishes anything, and CI has already run the tests on the PR and on master.

The very first release has nothing to PR: `version.txt` and the changelog section already say the version, so run
`just release 0.1.0` directly. Write the Unreleased notes before opening a release PR.

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
