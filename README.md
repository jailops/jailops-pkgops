# PkgOps

Build, sign, host and serve **native FreeBSD packages** from Linux, macOS or
anywhere Go runs. No FreeBSD builder, no ports tree, no poudriere.

```
Go cross-compile  ->  .pkg  ->  signed static repository  ->  nginx / GitHub Pages
                                                              |
                                           FreeBSD:  pkg install hello && pkg upgrade
```

PkgOps does not invent a package manager or a protocol. It writes the same
`.pkg` files and repository catalogs FreeBSD's own `pkg create` and `pkg repo`
write, so the stock `pkg(8)` on the other end - `pkg install`, `pkg upgrade`,
`pkg check`, signature verification - does all the real work. It is
independent software: pure Go, two dependencies (zstd and YAML), and it shares
no code or dependency with any other JailOps component.

Module `jailops.net/pkgops`, CLI `pkgops`, BSD 2-Clause.

## Install

Download the archive for your OS and CPU (linux, darwin, windows, freebsd; amd64,
arm64) from the [GitHub releases](https://github.com/jailops/jailops-pkgops/releases), check
it against `checksums.txt` (sha256), unpack it and put `pkgops` on your `PATH`:

```sh
v=0.1.0; curl -fsSLO https://github.com/jailops/jailops-pkgops/releases/download/v$v/pkgops_${v}_linux_amd64.tar.gz
curl -fsSLO https://github.com/jailops/jailops-pkgops/releases/download/v$v/checksums.txt
sha256sum --ignore-missing -c checksums.txt && tar -xzf pkgops_${v}_linux_amd64.tar.gz pkgops
./pkgops version
```

From source: `go install jailops.net/pkgops/cmd/pkgops@v0.1.0` (needs the vanity import
page on jailops.net, **UNVERIFIED**; see [docs/releasing.md](docs/releasing.md)), or
clone the repository and run `just build`. In GitHub Actions use
`uses: jailops/jailops-pkgops@v0.1.0` ([docs/hosting.md](docs/hosting.md)). There is no
release until `v0.1.0` has been cut.

## What PkgOps is for, and what it is not for

PkgOps is deliberately small. It packages **Go programs** (cross-compiled, no
cgo) together with **config files, rc.d scripts, users, groups and
directories**, and it ships them as a signed pkg repository. Its scripts are
plain `/bin/sh` snippets (pre/post install and deinstall).

It is **not** for: Lua scripts in package definitions, Rust, C, C++, cgo or any
other build system, native libraries, big ports trees, many interdependent
packages built from source, custom port options or bulk builds. Those need a
FreeBSD toolchain and a ports framework: **use poudriere** for them, and PkgOps
for the small things beside it. A manifest that asks for something outside this
scope (`build.type: rust`, a `lua` key, a `post-install.lua` script) is
rejected with an error that says so, not silently ignored.

## When to use PkgOps, and when to use poudriere

PkgOps aims at the roughly 95% of packaging jobs that are small: a Go daemon, a
single static binary, a CLI tool, some config files and an rc.d script. You
write 30 lines of YAML and have a signed repository in minutes, from a laptop or
a Linux CI runner.

**poudriere is the right tool for the other 5%**, and PkgOps does not try to
compete with it: building large parts of the ports tree, software that needs
CGO or native libraries (those need a FreeBSD toolchain and sysroot), many
interdependent packages built from source, custom port options, bulk builds
across jails and ports trees, `shlibs` and dependency resolution computed by
building. If your package needs a FreeBSD compiler, use poudriere (or the
ports framework) and use PkgOps for the small things beside it.

The two coexist. A PkgOps repository is just another `repos/*.conf` entry with
its own URL and catalog; see "Living next to poudriere" in
[docs/hosting.md](docs/hosting.md) (mainly: pick package names no port uses,
because pkg picks the highest version across enabled repositories).

## Quickstart: a Go daemon with an rc.d script

[examples/hello](examples/hello) is a tiny HTTP daemon with a config file, an rc.d
script and a service user.

```sh
go install ./cmd/pkgops            # or: just build

cd examples/hello                  # pkgops.yaml lists abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]
pkgops key generate --out /tmp/repo.key --bits 4096
SOURCE_DATE_EPOCH=1700000000 pkgops build -o dist       # go build + .pkg for EVERY ABI, in parallel
pkgops repo --repo site --key /tmp/repo.key dist        # signs <ABI>/latest for every ABI in one go
pkgops verify site/FreeBSD:15:amd64/latest --pubkey /tmp/repo.pub
pkgops publish --from site --to /var/www/pkg            # the whole tree; or: pkgops serve
# one architecture only:  pkgops build --abi FreeBSD:15:aarch64 -o dist   (or --abi a,b / --abi all)
```

On the FreeBSD machine, as root:

```sh
mkdir -p /usr/local/etc/pkg/keys /usr/local/etc/pkg/repos
fetch -o /usr/local/etc/pkg/keys/hello.pub https://pkg.example.org/repo.pub
cat > /usr/local/etc/pkg/repos/hello.conf <<'EOF'
hello: {
  url: "https://pkg.example.org/${ABI}/latest",
  signature_type: "pubkey",
  pubkey: "/usr/local/etc/pkg/keys/hello.pub",
  enabled: yes
}
EOF
pkg update
pkg install hello
sysrc hello_enable=YES
service hello start
```

(`service hello start` is **UNVERIFIED** on a real system; every other step of
the install was run against real pkg, see Status.)

## The manifest, `pkgops.yaml`

```yaml
name: hello
version: 0.1.0
origin: sysutils/hello            # category/name, default misc/<name>
comment: One line description     # required
desc: |                           # default: comment
  Longer text.
www: https://example.org          # required (pkg cannot index a package without it)
maintainer: you@example.org       # required
license: BSD2CLAUSE               # or licenses: [A, B] with license_logic: and|or
categories: [sysutils]
abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]   # built together; or a single abi: FreeBSD:15:amd64
                                  # pkgops build --abi a,b|all picks a subset
min_release: "15.1"               # oldest FreeBSD release it supports; pkg itself refuses an older
                                  # system (the ABI names the major only). or min_osversion: 1501000
prefix: /usr/local                # default

deps:                             # origin and version are both required by pkg
  - {name: pcre2, origin: devel/pcre2, version: "10.49"}

build:                            # optional; omit to package prebuilt files
  type: go                        # the only type
  package: ./cmd/hello            # one executable ...
  # packages:                     # ... or several in the same package (not with package/output)
  #   - {package: ./cmd/jailops}                     # output defaults to build/${ARCH}/jailops
  #   - {package: ./cmd/jailopsd, output: build/${ARCH}/jailopsd}
  ldflags: -s -w -X main.version=${VERSION}   # -buildid= is always added
  tags: []
  dir: .
  output: build/${ARCH}/${NAME}   # default

files:                            # destination relative = under prefix
  - source: build/${ARCH}/hello
    destination: bin/hello
  - source: packaging/hello.conf
    destination: etc/hello/hello.conf
    config: true                  # pkg @config: kept on upgrade, new copy as .pkgnew
  - source: docs                  # a directory is copied recursively
    destination: share/doc/hello
  - symlink: hello-0.1.0          # destination becomes a symlink to this target
    destination: bin/hello        # (relative or absolute; checksums match pkg's own rule)
  - content: "inline file\n"      # instead of source
    destination: share/hello/note
    mode: 0644                    # default 0755 if the source is executable, else 0644
    owner: root                   # default root:wheel
    group: wheel

services:                         # rc.d script -> etc/rc.d/<name>, mode 0555
  - {name: hello, source: packaging/rc.d-hello}

service:                          # or: generate the rc.d script, see "Generated services" in docs/format.md
  name: hello                     #   (replaces the hand-written script above for simple Go daemons)
  command: /usr/local/bin/hello
  user: hello                     # created by the package unless declared in users:
  restart: true                   # supervised by daemon(8) -r

dirs:                             # directories owned by the package
  - {path: /var/db/hello, owner: hello, group: hello, mode: 0750}

users:                            # created by a generated pre-install script (pw)
  - {name: hello, home: /var/db/hello, shell: /usr/sbin/nologin}   # uid optional
groups:
  - {name: extra}                # gid optional

scripts:                          # pre-install, post-install, pre-deinstall, post-deinstall
  post-install: |
    echo "enable with: sysrc hello_enable=YES"
  pre-deinstall: {file: packaging/pre-deinstall.sh}
```

Variables `${NAME} ${VERSION} ${ABI} ${ARCH} ${GOARCH}` expand in `source`,
`build.*`. Unknown keys and unknown variables are errors. Directories below the
prefix that hold your files (for example `share/hello`, `etc/hello`) are
claimed automatically so deinstall removes them when empty. Hardlinks are not
supported.

## CLI

```
pkgops build   [-f pkgops.yaml] [--abi a,b|all] [-o dist] [--no-go-build] [--epoch N]
pkgops inspect file.pkg                   verify a package, print its manifest
pkgops key     generate | public | fingerprint
pkgops repo    [--repo DIR] [--key repo.key] [--sign pubkey|fingerprints]
               [--files-list] [--replace] [--policy pkgops-repo.yaml] [--prune prune.yaml] [file.pkg ...]
pkgops fetch   --from URL|github-releases:OWNER/REPO --into DIR [--abi a,b] [--prune prune.yaml]
pkgops prune   --plan [--from URL|DIR] [--manifest prune.yaml] [--keep N | --older-than 90d | --name N --version V] [-n]
pkgops archive push --repo OWNER/REPO         archive every published .pkg in GitHub Releases
pkgops archive prune --repo OWNER/REPO ...    delete from the archive (separate, confirmed, audited)
pkgops verify-archive --repo OWNER/REPO --from URL|DIR
pkgops verify  <repo-dir> [--pubkey repo.pub]
pkgops publish --from DIR --to DEST [--delete] [--ssh "ssh -i key"]
pkgops conf    --name NAME --url URL [--pubkey PATH | --fingerprints DIR] [--client-cert P --client-key P [--ca-cert P]]
pkgops pki     init | issue server|client | revoke | list | crl | show      private CA for a private registry
pkgops serve   --repo DIR [--listen ADDR|unix:PATH] [--token-file F] [--key K] [--keep N] [--ui]
               [--tls-cert F --tls-key F [--client-ca F [--crl F]]]        HTTPS, optionally mTLS-only
pkgops serve   --source URL [--prune-via github:OWNER/REPO[@branch]:prune.yaml] [--workflow publish.yml]
               [--access-team T --access-aud A --access-allow FILE]   admin UI over a published site
pkgops serve   token --name NAME [--role viewer|uploader|admin]
pkgops registry status|list|show|upload|delete --url URL [--token T]
pkgops push    --url URL --token T [--client-cert C --client-key K --ca-cert CA] file.pkg ...
```

`build` cross-compiles with `GOOS=freebsd GOARCH=amd64|arm64 CGO_ENABLED=0
-trimpath -buildvcs=false -buildid=`, then writes
`dist/<ABI>/<name>-<version>.pkg`. Package content is stamped with
`--epoch`, else `SOURCE_DATE_EPOCH`, else the newest source mtime. Two builds
of the same inputs are byte-identical (tested, including the Go binary) when built
by the same Go: the build runs with `GOTOOLCHAIN=auto` unless you set it, so
`go.mod`'s `go`/`toolchain` lines choose the Go, the version used is printed,
and the resulting static binaries never need Go on the FreeBSD host.

Published sites keep every version: `pkgops repo` writes `pkgops-index.json` next to
the pkg catalog (the catalog still lists only the newest version, as pkg expects),
`pkgops fetch` rebuilds the baseline from it, and `pkgops prune --plan` writes a
reviewed `prune.yaml` that the next build applies. Nothing is ever deleted by
itself. See [docs/hosting.md](docs/hosting.md) and [docs/admin.md](docs/admin.md).

## Go SDK

The CLI is a thin wrapper over the importable package `jailops.net/pkgops`
(see `go doc jailops.net/pkgops` and the runnable `Example` tests):

```go
spec, _ := pkgops.LoadSpec("pkgops.yaml")
built, err := pkgops.BuildPackage(ctx, spec, pkgops.BuildOptions{OutDir: "dist"})

signer, _ := pkgops.LoadRSASigner("repo.key", pkgops.SignPubkey)
repo := &pkgops.Repository{Root: "site", Signer: signer}
_, err = repo.Update(ctx, built.Path)             // add + re-index + sign
_, err = pkgops.VerifyRepository(repo.Dir("FreeBSD:15:amd64"), pubPEM)
err = pkgops.Publish(ctx, "site", "/var/www/pkg", pkgops.PublishOptions{})
```

You can skip the manifest and fill a `pkgops.Package` with in-memory file
contents. Errors wrap sentinels (`ErrInvalidSpec`, `ErrInvalidPackage`,
`ErrSignature`, `ErrNotFound`, `ErrUnsupportedABI`, `ErrBuild`); long operations
take a `context.Context`. The optional registry is `jailops.net/pkgops/serve`; `jailops.net/pkgops/sdk`
is a small Go client for its HTTP API (upload, list, show, delete, status).
The API is pre-1.0: it may still change.

## Hosting

- Any static server: `pkgops publish` (local dir, or rsync over ssh).
- `pkgops serve`: static serving of the repository plus a token-protected API
  (roles viewer, uploader, admin) that uploads, deletes and re-signs without
  ever exposing a catalog that points at a missing package, with retention and
  an optional web UI (overview, search, package details, repository status,
  sign-in with a token held in memory, delete with confirmation). `sdk/` is the
  Go client for that API.
- GitHub Pages or Releases from Actions with the signing key in a secret.

Details, nginx snippet and client configuration: [docs/hosting.md](docs/hosting.md).
A private registry for your own machines (HTTPS with per-machine client
certificates, individually revocable, plus the normal repository signature):
[docs/private-registry.md](docs/private-registry.md).
Format notes (what a `.pkg` and a catalog contain, the signature convention,
gotchas that make `pkg update` fail): [docs/format.md](docs/format.md).

## Status

Developed against FreeBSD 15.1-RELEASE (arm64), pkg 2.8.4. PkgOps targets
FreeBSD 15 and newer; older releases and older pkg are untested.

**Verified against real pkg** (FreeBSD 15.1 arm64, private root directories, a
private nginx, a chroot for user creation):

- `pkg info -F`, `pkg add`, `pkg check -s`, `pkg delete` on our `.pkg`; `pkg
  repo` and `pkg repo -l` read it, and the catalog `pkg repo` writes for it
  equals ours except the checksum algorithm and the `All/` prefix.
- `pkg update`, `install`, `upgrade` from a static repository over HTTP:
  0.1.0 -> 0.1.1, dependencies, `@config` handling (modified file kept with
  `.pkgnew`, pristine file replaced, removed on deinstall).
- Signature verification both ways, `pubkey` and `fingerprints`: good signature
  installs; wrong key, tampered catalog and tampered package are all rejected.
- Users and groups created by the generated pre-install script, directory
  ownership, directories removed on deinstall (chroot install).
- rc.d script sourced by `rc.subr`; the cross-compiled binary runs natively.
- `pkgops serve` and `pkgops push` (and pkgops itself, cross-compiled) running
  on FreeBSD, with re-signing after each upload and retention.
- `files.pkg` accepted by `pkg update`; `pkg rwhich` uses it.
- Symlinks (relative, absolute, dangling, to directories): `pkg add`, `pkg check
  -s` (clean passes, a retargeted link is reported), `pkg delete`. The checksum
  rule for absolute links was recovered from FreeBSD's own packages.
- Registry on FreeBSD: roles enforced (viewer and uploader refused delete,
  viewer refused upload), admin delete, then real `pkg update` sees the older
  version and `pkg fetch` of it works; deleting the last version yields an
  empty catalog that `pkg update` accepts; the web UI is served from the binary.

**UNVERIFIED**:

- Boot-time (a real reboot) behaviour of rc.d scripts, and `service start` of a
  hand-written script. The generated `service:` script was run through
  start, status, restart, stop, kill -9 and `pkg delete` on FreeBSD 15.1
  (arm64 and amd64), and `rcorder` placed it after DAEMON; see docs/format.md.
- amd64 packages on real amd64 hardware (built, checked as FreeBSD x86-64 static
  ELF, structurally identical to the aarch64 ones that were installed).
- The GitHub Action, Pages, Releases and the `FreeBSD:15:amd64` directory names on
  GitHub Pages (the publish script was run against a local bare git repo only).
- rsync-based `publish` (no rsync on the available machines).
- ECDSA/EdDSA signing, `pkg+https://` SRV mirrors, pkg other than 2.8.4.
- Install onto a real root filesystem as opposed to `pkg -r` roots and a chroot.
- The vanity import path `jailops.net/pkgops` (needs a go-import meta tag served
  from jailops.net).

## Limits

- **No CGO / native libraries.** Pure Go with `CGO_ENABLED=0` only. Anything
  needing a FreeBSD C toolchain or sysroot needs a real builder.
- No shared-library tracking (`shlibs_required`/`provided`): not needed for static
  Go binaries, wrong for anything else you package by hand.
- No hardlinks, no `lua_scripts`, triggers, `@sample`, annotations,
  options, categories-as-directories; only `tzst` packages.
- Only the newest version per name is advertised in a catalog (pkg repo keeps all).
- Package checksums are `1$` SHA-256 rather than pkg's `2$` BLAKE2 (accepted by
  pkg, documented in docs/format.md).
- amd64 and aarch64 only.
- Single writer per repository directory.
- `pkgops serve` speaks plain HTTP: terminate TLS in front of it.

## Layout

```
pkgops.go                     public API of the root package (re-exports internal/*)
internal/pkgfile              .pkg writer/reader, ABI, symlink and checksum rules
internal/build                pkgops.yaml, manifest -> package, go build
internal/repository           catalogs, files.pkg, signing, versions, verification
internal/publish              local and rsync publishing
internal/registry             uploads, delete, retention, status (no HTTP)
internal/server               HTTP API, static serving, embedded UI hook
internal/config               tokens and roles
internal/webui                go:embed of the built UI (always compiles)
serve/                        public facade of the registry server
sdk/                          Go client for the registry API
web/                          Vue 3 + TypeScript + Vite UI (source and tests)
cmd/pkgops/                   CLI
examples/hello/               end-to-end example (own go.mod)
testdata/                     golden files and a real pkg-created package
docs/                         format.md, hosting.md, private-registry.md
action.yml scripts/           GitHub Action and its script
```
