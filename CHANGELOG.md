# Changelog

All notable changes are listed here, in the style of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Each release has a section `## [X.Y.Z] - DATE`; `just release` moves the Unreleased entries under the new version
and `.goreleaser.yaml` publishes the GitHub release notes from the commits.

## [Unreleased]

## [0.2.1] - 2026-11-02

### Security
- `pkgops fetch --pubkey repo.pub [--pubkey-sha256 HEX]` verifies the published site's signed catalogs (`data.pkg`
  and `packagesite.pkg`) against a key from a trusted place before it writes anything, and refuses an unsigned
  index that disagrees with them: a changed newest package, an entry the catalog does not advertise, a version above
  the advertised one, a prune that would make an unsigned version the newest. Before, the baseline that the next
  `pkgops repo --key` signs was checked only against the unsigned `pkgops-index.json` of the same site. Older
  versions remain covered by no signature (the catalog lists the newest only); limits and a design note for a
  signed index are in `docs/hosting.md`, "Verified baseline". Without `--pubkey` nothing changes.
- Downloads by `pkgops fetch` stop at the expected size (a longer body fails at once and leaves nothing behind),
  check `Content-Length` and the `Content-Range` of a resumed download, and have an idle timeout (60 s) and an
  overall deadline scaled to the size. Archive downloads stop at the size their manifest lists.

### Fixed
- `pkgops serve --source`: the status was "Healthy" and "Signed" without any verified signature when the site
  publishes its key under another name than `pkgops.pub`, or the key was missing or invalid. It now checks both
  catalogs and reports `verification` (`verified`, `unverified`, `invalid`, `unsigned`, `signed`), the key's
  fingerprint and where the key came from. The key is `--pubkey FILE`, a site key pinned by `--key-fingerprint`,
  or the first of `pkgops.pub`, `jailops.pub`, `repo.pub` on the site (`--key-path`), the last shown as a weaker
  trust. `--require-verified` (implied by the first two) makes anything but `verified` unhealthy.
- `pkgops serve`: the cached verification of the status page now depends on the sha256 of every package, so a
  package replaced by other bytes of the same name and version shows at once; a cached result is also re-checked
  after 10 minutes.
- Web UI: the client configuration used the address of the admin server, which in `--source` mode does not serve
  the repository. The API now returns `download_url` and `key_url` (`--download-url`, `--key-url`; default the
  `--source` site), the UI uses them, and shows upload steps only where uploads go to this server.

### Added
- `scripts/check-actions-pinned.sh` and a CI step: third-party actions must be pinned to a commit SHA (warning
  until `ACTIONS_PIN_ENFORCE` in `ci.yml` is `"1"`). `docs/supply-chain.md` has the commands. `release.yml` grants
  `contents: write` to its one job only.

## [0.2.0] - 2026-10-27

### Changed
- Releases are PR-based: `just release-pr X.Y.Z` opens the version PR, `just release X.Y.Z` signs and pushes the tag after the merge. The repository is `jailops/jailops-pkgops` (the Go module path stays `jailops.net/pkgops`).
- **`pkgops serve` listens on port 39112 by default** (it was 8080), the port of the JailOps family (39110 to 39112, see docs/ports.md of jailops). **The web UI is in the binary and off until you add `--ui`** (it was on, with `--no-ui` to turn it off).

## [0.1.0] - 2026-10-26

### Added
- Private registry: `pkgops pki` (offline-capable CA in pure Go: init, issue server/client, revoke, list, crl, show),
  `pkgops serve --tls-cert/--tls-key [--client-ca [--crl]]` (certificates, CA and CRL reload without a restart;
  revocation also applies to resumed sessions and open keep-alive connections; fail closed),
  `--listen unix:PATH`, client-certificate flags for `pkgops push|registry`, and `pkgops conf --client-cert
  --client-key --ca-cert` (the `env` block, verified with pkg 2.8.4). See `docs/private-registry.md`.
- Minimum FreeBSD release: `min_release: "15.1"` (or `min_osversion`) in `pkgops.yaml` is written as the
  `FreeBSD_version` annotation, which pkg(8) enforces on the client (verified live with pkg 2.8.4; see
  `docs/format.md`). Repository policy `pkgops-repo.yaml` (supported ABIs, minimum release, retention hint) is
  honoured by `pkgops repo`, `publish` and `serve`.
- `pkgops build` runs `go` with `GOTOOLCHAIN=auto` (unless set) and prints the Go version used.
- `build.packages` builds several executables into one package; `build.package` still works.
- `pkgops-index.json` lists every published file with sha256 (the pkg catalog is unchanged: newest version only);
  `pkgops fetch` rebuilds the baseline from the site or from the archive, with sha256 verification and resume.
- `prune.yaml` and `pkgops prune --plan`: cleanup is a reviewed plan, never an automatic deletion; the newest
  version needs `force`.
- `pkgops archive push|prune`, `pkgops verify-archive`: GitHub Releases as the permanent archive.
- `pkgops serve --source URL`: read-through admin service with GitHub App cleanup requests, republish,
  Cloudflare Access authentication with an explicit role allow-list, and an admin "Manage" view in the web UI.
  See `docs/admin.md`. GitHub and Cloudflare are tested against fakes only.
- Manifests that ask for something outside PkgOps' scope (other build types, Lua) now fail with a message that
  names poudriere. README says what PkgOps is and is not for.
- Release management: `just release vX.Y.Z`, GoReleaser archives for linux, darwin, windows and freebsd
  (amd64, arm64), and a GitHub release workflow on `v*` tags.
- `pkgops version` prints the version, commit and build date.
- The GitHub Action downloads the `pkgops` binary of its own tag from the GitHub release (sha256 verified) and
  falls back to building from source.
- `service:` in `pkgops.yaml` generates a conventional rc.d script (rc.subr, supervised by daemon(8) with optional
  `-r`/`-R` restart, stdout/stderr to a log file or syslog, optional newsyslog fragment), creates the service
  user, and stops the service on `pkg delete`. See `docs/format.md` and `examples/hello/pkgops-service.yaml`.
  The hand-written `services:` list still works and may be combined with it.
