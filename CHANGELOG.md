# Changelog

All notable changes are listed here, in the style of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Each release has a section `## [X.Y.Z] - DATE`; `just release` moves the Unreleased entries under the new version
and `.goreleaser.yaml` publishes the GitHub release notes from the commits.

## [Unreleased]

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
