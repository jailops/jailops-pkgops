# Hosting a repository

A pkgops repository is a directory of static files. Pick the hosting that fits;
all of them serve the same tree.

Status of each path is marked **verified** (exercised against real FreeBSD
pkg 2.8.4) or **UNVERIFIED**.

## 1. Any static web server (nginx)  - verified

```sh
pkgops key generate --out repo.key          # once; keep repo.key secret
pkgops build -o dist                        # in your project
pkgops repo --repo site --key repo.key dist/*/*.pkg
pkgops verify site/FreeBSD:15:amd64/latest --pubkey repo.pub
pkgops publish --from site --to /var/www/pkg
```

nginx needs nothing special:

```nginx
server {
    listen 443 ssl;
    server_name pkg.example.org;
    root /var/www/pkg;
    location ~ /All/.*\.pkg$ { add_header Cache-Control "public, max-age=31536000, immutable"; }
    location ~ \.pkg$|/meta(\.conf)?$ { add_header Cache-Control "no-cache"; }
}
```

(Verified with a private nginx 1.30 on FreeBSD serving the tree over HTTP to
`pkg update` / `install` / `upgrade`. The `add_header` lines are optional.)

`pkgops publish` copies packages first and catalogs last, each file atomically,
skips unchanged files, and with `--delete` removes stale ones. A client that
fetches while you publish sees either the old catalog (whose packages still
exist) or the new one. With a remote destination it runs rsync over ssh in
three passes (packages, catalogs, optional delete):

```sh
pkgops publish --from site --to deploy@pkg.example.org:/var/www/pkg --ssh "ssh -i deploy_key"
```

The local-directory path is verified (tests); the rsync path is **UNVERIFIED**
(no rsync was available on the test host or the dev machine).

## 2. `pkgops serve` (optional registry)  - verified on FreeBSD

```sh
pkgops serve token --name ci --role uploader     # prints a secret and a token-file line
pkgops serve token --name alice --role admin
# put the printed "name role sha256" lines into the token file, then:
pkgops serve --repo /var/pkg --listen 127.0.0.1:39112 \
     --token-file tokens --key repo.key --sign pubkey --keep 5
pkgops registry upload --url https://pkg.example.org --token "$PKGOPS_TOKEN" dist/*/*.pkg
pkgops registry status --url https://pkg.example.org
```

**Roles.** `viewer` can sign in to the UI and change nothing; `uploader` can
upload; `admin` can upload and delete. The scopes `upload` and `delete` can be
listed instead of a role. Deleting needs an admin token. Tokens are stored as
SHA-256 only. With no token file the server is read-only.

**Reads.** The repository directory is served as plain static files with
correct content types, `Last-Modified`/ETag and range requests; packages are
`immutable`, catalogs `no-cache`; no directory listings, dotfiles hidden. nginx
on the same directory is an equivalent read path. `serve` speaks plain HTTP unless
you give it `--tls-cert/--tls-key`; for a private registry with client
certificates see [private-registry.md](private-registry.md).

**API** (`/api/v1`, JSON; Go client in `jailops.net/pkgops/sdk`):

| Call | Auth | Does |
|---|---|---|
| `GET /health`, `/status`, `/packages[?abi=]`, `/packages/{abi}/{name}/{version}`, `/key` | none | overview, listing, manifest content, public key |
| `GET /session` | any token | who am I (role) |
| `PUT /packages` (body: `.pkg`) | uploader | verify, add, re-index, re-sign, apply `--keep` |
| `DELETE /packages/{abi}/{name}/{version}` | admin | catalog first, then the file |

Statuses: 201 created, 204 deleted, 401 missing or invalid token, 403 role too
low, 404, 409 same name-version with different bytes, 422 invalid package.

**Why a client never sees a broken repository.** There is no database: the
directory is the state. Catalog files are built in a staging directory
(`.stage-*`) and renamed into place one by one; packages are copied in before
the catalog that lists them; on delete the catalog is rebuilt *without* the
package before the file is removed. So at every instant every catalog file on
disk is complete, signed, and lists only packages that exist. A process killed
at any step (tested by killing a helper process at each step of add and delete)
leaves a consistent repository; leftovers (staging dirs, an unlisted file) are
cleaned by the next operation. It is not one transaction across files: a client
that read the catalog just before a delete may get a 404 for the removed file
and recovers on its next `pkg update`. Single writer process only.

**Web UI** (`/ui/`, in the binary and off by default, `--ui` to serve it): overview (health,
counts, platforms), package search and detail (files, symlinks, config files,
dependencies, versions), repository status (signature, fingerprint, per-catalog
consistency, damage reported in words), sign-in with a token kept in memory
only (a reload signs out; nothing is stored), upload and delete with a typed
confirmation. Light and dark, responsive, keyboard and screen-reader friendly,
served with a strict CSP and no external assets. It is built from `web/` into
`internal/webui/dist` and embedded in the binary: no Node at runtime, and a
build without the assets compiles and works without the UI.

Also: pkg decides whether a catalog is new by its modification time with
one-second granularity, so two catalog updates inside the same second can be
missed by a client that updated in between; it picks the change up on the next
update.

## 3. GitHub: Pages or Releases, no FreeBSD machine  - UNVERIFIED on GitHub

`action.yml` at the repository root is a composite action; it builds pkgops from
its own checkout, runs `scripts/gh-publish.sh`, and publishes. The script itself
was run end to end here against a local bare git repository (build for two ABIs,
sign, update the `gh-pages` branch, re-run); the GitHub-specific parts (Pages
serving, token permissions, `gh release`) have not been exercised on GitHub.

```yaml
# .github/workflows/release.yml  (also in examples/hello/release.workflow.yml)
on: { push: { tags: ["v*"] } }
permissions: { contents: write }
jobs:
  pkg:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - uses: jailops/jailops-pkgops@v0.1.0   # the first release must exist before this works
        with:
          # abis: omitted = every ABI in pkgops.yaml, all built in this one step
          signing-key: ${{ secrets.PKG_SIGNING_KEY }}
          publish: pages        # or: release | none
```

Pin the action to a release tag (or a full commit SHA); the action runs with
your signing key in its environment, so never track `@master` and review
changes before bumping the pin. **The first release (`v0.1.0`) must be created
before this example works** (see [releasing.md](releasing.md)): until the tag
and its GitHub release exist, `uses: jailops/jailops-pkgops@v0.1.0` cannot resolve.

For a tag `vX.Y.Z` the action downloads `pkgops_X.Y.Z_linux_<arch>.tar.gz` from
that GitHub release (linux amd64 or arm64 runners), verifies it against the
release's `checksums.txt` (sha256) and falls back to building the action's own
checkout with `go build` if the download or the check fails. A branch or SHA
pin is built from source. `scripts/install-pkgops.sh` does this and
`scripts/gh-publish.sh` uses it when `PKGOPS_VERSION` is set and `pkgops` is not
on `PATH`. The download path is **UNVERIFIED** on GitHub until the first release exists.

One-time setup: `pkgops key generate --out repo.key`, then
`gh secret set PKG_SIGNING_KEY < repo.key`, enable Pages for the `gh-pages`
branch. The action also stores the public key at `pkgops.pub` on that branch.

- `publish: pages` keeps a signed repository on a git branch. Repository URL:
  `https://<owner>.github.io/<repo>/${ABI}/latest`. `.nojekyll` is added. Things
  to know: every version adds binary blobs to the branch history (use `keep`,
  and squash the branch occasionally); Pages has size and bandwidth limits;
  directory names contain `:` (`FreeBSD:15:amd64`), which git and Pages should
  handle but is **UNVERIFIED**. Pages is fine for small projects; for heavy use
  rsync to your own server or use `pkgops serve`.
- `publish: release` attaches `name-version-<abi>.pkg` files to a GitHub
  release. That is not a pkg repository (no catalog, no `pkg upgrade`); users
  install with `pkg add https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>`.
  `pkg add` verifies the package structure but there is no signature on a bare
  package.
- Re-running a workflow for the same version fails if the bytes differ (Go
  version change, different timestamp): published versions are immutable. Set
  `replace: true` to overwrite, knowing clients that cached the old file will
  report a checksum mismatch until they `pkg clean`.

## Production layout: Pages distributes, PkgOps manages the lifecycle

For a packages repository that lives on GitHub (for example jailops-packages):

```
 packages repo (git): sources, pkgops.yaml files, pkgops-repo.yaml, prune.yaml   <- reviewed, small
   |  build workflow
   v
 fetch baseline  ->  build new packages  ->  pkgops repo (policy, prune.yaml)  ->  archive push
   |                                                                               (GitHub Releases: every .pkg, forever)
   v
 upload-pages-artifact  ->  deploy-pages   ==>  GitHub Pages: pkg.example.org/${ABI}/latest   <- what pkg installs from
```

- **Do not keep binaries in `gh-pages` git history**: git keeps every old blob
  forever, so a branch that serves packages only grows. Pages has no history
  of its own either, so the site is an *artifact* rebuilt each time from the
  baseline (`pkgops fetch` from the published site) plus the new packages, then
  deployed with `actions/deploy-pages`. No binary ever lives in git.
- **Releases are the permanent archive**: `pkgops archive push` stores every
  published `.pkg` (idempotent, sha256 checked). If the Pages baseline is ever
  lost or damaged, `pkgops fetch --from github-releases:OWNER/REPO` restores it.
- **Pruning removes from Pages only**, through the committed `prune.yaml` and the
  next deploy; deleting from the archive is a separate, confirmed action.

```yaml
# .github/workflows/publish.yml in the packages repository (UNVERIFIED on GitHub)
on: { push: { branches: [master] }, workflow_dispatch: {} }
permissions: {}
concurrency: { group: publish, cancel-in-progress: false }
jobs:
  publish:
    runs-on: ubuntu-latest
    environment: signing          # the key is a secret of this environment only
    permissions: { contents: write }   # contents: archive releases
    steps:
      - uses: actions/checkout@v4
        with: { persist-credentials: false }
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: sh scripts/install-pkgops.sh /usr/local/bin   # the release binary, sha256 verified (README, Install)
      - name: baseline from the published site, verified against the committed public key
        run: pkgops fetch --from https://pkg.example.org --into site --prune prune.yaml --source auto --pubkey repo.pub
        env: { PKGOPS_ARCHIVE_REPO: ${{ github.repository }}, GITHUB_TOKEN: ${{ github.token }} }
      - name: build the packages
        run: pkgops build -f main/hello/pkgops.yaml -o dist
      - name: sign (the only step that has the key on disk)
        env: { KEY: ${{ secrets.PKG_REPO_KEY }} }
        run: |
          umask 077; k="$RUNNER_TEMP/repo.key"; trap 'rm -f "$k"' EXIT
          printf '%s\n' "$KEY" > "$k"
          pkgops repo --repo site --policy pkgops-repo.yaml --prune prune.yaml --key "$k" dist
      - run: pkgops verify site/FreeBSD:15:amd64/latest --pubkey repo.pub
      - run: pkgops archive push --repo ${{ github.repository }} --from site
        env: { GITHUB_TOKEN: ${{ github.token }} }
      - uses: actions/upload-pages-artifact@v3
        with: { path: site }
  deploy:
    needs: publish
    runs-on: ubuntu-latest
    environment: { name: github-pages }
    permissions: { pages: write, id-token: write }
    steps:
      - uses: actions/deploy-pages@v4
```

The index, the policy and the prune manifest are part of the site, so the next
run (and the admin UI) see them. `pkgops fetch` needs the published site to carry
`pkgops-index.json`; the first run of a new site has none, and `--source auto`
then falls back to the archive (an empty archive on the very first run is
"no baseline": start with `pkgops repo --repo site ...` instead of `fetch`).
The older `publish: pages` mode of the action (a `gh-pages` branch) keeps
binaries in git history and is **not recommended** for a repository with many
versions.

### Verified baseline

`pkgops-index.json` is not signed, and the next `pkgops repo --key` signs new
catalogs over whatever the baseline holds. Without a check, whoever controls
the *delivery* of the site (DNS, a CDN, the web server), but not the key, could
change a package and its index entry and have your next normal release sign it.

`pkgops fetch --pubkey repo.pub` closes that for everything a signature covers.
Before it writes anything it downloads `data.pkg` and `packagesite.pkg` of every
ABI in scope and:

- verifies both signatures against `repo.pub`, which must come from a trusted
  place: your checkout, or a file checked against a committed fingerprint
  (`--pubkey-sha256`). Never take it from the site you are verifying;
- requires every package the signed catalog advertises to be in the index with
  the same file, sha256 and size, and verifies the download against that hash;
- refuses an index entry for a package name the catalog does not advertise, or
  a version at or above the advertised one: those are exactly the entries the
  next run would advertise and sign;
- refuses a prune manifest that drops the newest signed version while older
  versions stay (an unsigned one would become the newest); prune every version
  of that package, or publish a new version instead;
- refuses an index without signed catalogs, a catalog signed with another key,
  and a catalog in a format it cannot compare (pkg's own `2$` sums).

Limits, plainly:

- **History is not signed.** A pkg catalog lists only the newest version of
  each package, so older versions are checked against the index alone, as
  before (`fetch` counts them as "no signature"). They are never advertised or
  signed by the next run, but a changed old file would be carried on and served
  again. The GitHub Releases archive is an independent second copy:
  `pkgops verify-archive --from site` before signing reports a mismatch.
- **No rollback serial.** The catalogs carry no signed counter or date (the tar
  mtime is not covered by the signature). A complete older snapshot of the site,
  catalogs and index of an earlier day, verifies. Its newest versions are
  genuine, signed earlier; a pipeline that builds every current version from its
  sources replaces them, and `verify-archive` reports versions that were
  published and are now missing. Packages that are not rebuilt keep the older
  signed version.
- The first publish has nothing to verify: `fetch` answers "no baseline", and
  starting from an empty site is an explicit step (see the workflow above).
- The archive restore path (`--source releases`, or `auto` when the site has no
  index) has no signed catalog; it is trusted as far as the GitHub repository is.

**Design note, not implemented** (it changes what pkgops signs): a detached
signature over `pkgops-index.json` (`pkgops-index.json.sig`, RSA-SHA256 over the
hex sha256 of the file, the convention of the catalogs) made by `pkgops repo`
with the same key, and a monotonic `serial` field in the index raised by every
`pkgops repo`. `fetch --pubkey` would then verify the whole history against the
key and refuse a serial lower than one the checkout records (for example a
`PUBLISHED_SERIAL` file the publish job commits, or the serial of the last
deploy kept as a workflow variable). The catalogs pkg reads would not change.
Until then the archive is the cross-check for history.

Admin UI over such a site: see [admin.md](admin.md).

## Client configuration

Public key distribution is out of band: ship `repo.pub` with your docs, a
config-management role, or an initial `fetch`. Over plain HTTPS the first key
download is your trust anchor.

`/usr/local/etc/pkg/repos/myrepo.conf`:

```
myrepo: {
  url: "https://pkg.example.org/${ABI}/latest",
  signature_type: "pubkey",
  pubkey: "/usr/local/etc/pkg/keys/myrepo.pub",
  enabled: yes
}
```

or, with `--sign fingerprints`, `signature_type: "fingerprints"`,
`fingerprints: "/usr/local/etc/pkg/fingerprints/myrepo"` and a file
`trusted/myrepo` made by `pkgops key fingerprint repo.pub` inside that directory.
`pkgops conf --name myrepo --url ... --pubkey ...` prints the block. Then:

```sh
pkg update
pkg install hello
pkg upgrade
```

## Living next to poudriere and the official repositories

pkgops output does not conflict with other repositories:

- Each repository is a separate `repos/*.conf` entry with its own URL, so
  catalogs, caches (`/var/db/pkg/repos/<name>`) and signatures never mix. The
  `<ABI>/latest` layout is the one pkg.FreeBSD.org uses and does not collide with
  a poudriere tree (`<jail>-<ports>/` under `data/packages`).
- pkg picks the **highest version among all enabled repositories**, regardless
  of order. Give your packages names that no port uses (or use `pkg install -r
  myrepo name`), and consider `CONSERVATIVE_UPGRADE`, or the official `hello`
  could replace yours. Use distinct `origin` categories (e.g. `local/hello`).
- `PRIORITY` in the repo conf breaks ties between equal versions only.
