# PkgOps - working notes

Standalone Go toolkit (module `jailops.net/pkgops`, CLI `pkgops`) that builds
native FreeBSD `.pkg` files and pkg repositories without a FreeBSD host.

## Rules

- Independent project: never import or copy code from other JailOps components
  and do not mention sibling projects. Dependencies stay minimal: only
  `github.com/klauspost/compress` (zstd) and `gopkg.in/yaml.v3`. Everything else is stdlib.
- Targets FreeBSD 15.1+ and pkg 2.x. Do not add support for older releases.
- Ground truth is real `pkg`. Format decisions come from the man pages and
  from output of `pkg create` / `pkg repo` on a FreeBSD host (docs/format.md).
  When touching the .pkg or catalog writers, re-verify against real pkg and
  keep `testdata/real/*` and the golden tests honest.
- Never claim something works on FreeBSD unless a real pkg ran it. Mark the
  rest UNVERIFIED in README/docs.
- Determinism is a feature: no map iteration order, no wall-clock time, no
  random data in package or catalog bytes (zstd encoder pinned to concurrency 1).
- No FreeBSD CI is claimed; CI is Linux build/test only.

## Layout

- The root package `pkgops` is a thin facade (`pkgops.go`): the same exported names as
  always, implemented in `internal/`. Do not add logic there. Same for `serve/`.
- `internal/pkgfile` .pkg writer/reader, ABI, symlink rules; `internal/build` manifest and go build;
  `internal/repository` catalogs, signing, verification, add/delete/prune; `internal/publish`;
  `internal/registry` registry operations (no HTTP); `internal/server` HTTP; `internal/config` tokens and roles; `internal/pki` private CA (docs/private-registry.md); `internal/mtls` TLS/mTLS/CRL reload for serve;
  `internal/webui` go:embed of the `web/` output (dist/.gitkeep keeps it compiling);
  `internal/testutil` shared test fixtures.
- `sdk/` is the Go client and the wire types of the registry API. `cmd/pkgops/` is the CLI (thin).
- `web/` is the Vue 3 + TypeScript + Vite UI. `npm run build` writes into `internal/webui/dist` (commit the
  result); tests: `npm test` (vitest) and `npm run e2e` (Playwright against a real seeded `pkgops serve`,
  needs Chromium). Inline `style=` attributes are forbidden: the UI is served with a strict CSP.
- `examples/hello/` has its own go.mod; build it with the `pkgops` binary, not `go run`.

## Invariants of the repository directory (registry)

- Never overwrite or remove a file a published catalog refers to before a catalog without it is in place.
- Packages are copied in before the catalog that lists them; deletes rebuild the catalog first.
- Everything under the channel dir that starts with `.` is private (staging, temp) and never served.
- ABI strings from the network must pass `checkABI` before they touch the filesystem.

## Commands

```
just test        # go test ./...  (cross-compiles for FreeBSD, ~30 s the first time)
just ci          # gofmt check, vet, race tests
just web         # web: typecheck, vitest, build into internal/webui/dist
just e2e         # Playwright e2e + a11y audit (builds a seeded registry)
just golden      # go test -run Golden -update .   (then review the diff)
```

## Testing on a FreeBSD host

Use only a private `mktemp -d` under /tmp, `pkg -r <root>` (root dir must be
`root:wheel`; seed `etc/master.passwd` + `pwd_mkdb`, `etc/group`), `-o REPOS_DIR=`,
`-o PKG_CACHEDIR=`, a private nginx on a high port with its own pid/log paths.
`pkg -c <chroot>` with copies of `sh`, `pw`, `pwd_mkdb` and their libs tests
user creation. Clear the pkg cache between tamper tests (it is checksum-validated).
Set `-u` in scripts, guard every `rm -rf`, never `pkill -f`; do not touch
services, pf, jails, ZFS datasets or system pkg repo configs.

## Known pkg gotchas (details in docs/format.md)

- `desc`, `maintainer`, `www` NOT NULL in pkg's sqlite: missing -> `pkg update` fails for everyone.
- Dependencies need `origin` and `version` or pkg skips them.
- Signature = RSA-SHA256 over the hex string of the catalog's sha256 (+ NUL in `signature`).
