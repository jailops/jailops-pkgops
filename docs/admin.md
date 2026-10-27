# Administering a published repository

`pkgops serve --source URL` is a small admin service for a repository that is
published as a static site (GitHub Pages) from a packages repository. It reads
the published site, shows every version, and turns "remove this version" into a
reviewed commit. It has no database and does not replace the site.

```
Admin -> Cloudflare Access -> pkgops serve (UI + API) -> GitHub API (GitHub App)
       -> packages repo (prune.yaml commit / workflow_dispatch) -> GitHub Actions
       -> GitHub Pages -> pkg.example.org            (GitHub Releases = permanent archive)
```

Everything below that touches real GitHub or Cloudflare is **UNVERIFIED**: no
GitHub or Cloudflare account was reachable while this was written. It is tested
against fakes only (an `httptest` GitHub API, a fake Cloudflare JWKS endpoint).

## What it can do

- Show the policy (supported ABIs, minimum release), the signature and
  fingerprint status, and for every ABI every package and **every version**
  (from `pkgops-index.json`) with size, built time, minimum release, and a state:
  *on Pages*, *pending prune*, *pruned from Pages*, and the archive state
  (*archived*, *not archived*, *pruned from Pages but still archived*,
  *archive deleted*).
- **Request a cleanup**: add entries to `prune.yaml`. With GitHub configured it
  commits the file (`Prune: remove hello 0.1.0 (FreeBSD:15:amd64) requested by
  alice@example.com`); without it the UI shows the diff, the file and the
  commands to apply by hand. **Nothing is deleted**; the next deploy leaves the
  files out. The states shown are *requested -> committed -> workflow running ->
  published*; the last one is confirmed by polling the published index.
- **Republish**: trigger the publish workflow (`workflow_dispatch`) and show its
  recent runs.
- **Delete from the archive**: separate from pruning, see below.

## Who may do what (PkgOps decides, not Cloudflare)

Cloudflare Access only says *who* the caller is. PkgOps maps that identity to a
role through its own allow-list and checks the role itself before any GitHub
action:

| role | can |
|---|---|
| viewer | read the overview |
| uploader | read, upload (local `--repo` mode) |
| admin | everything: cleanup requests, republish, archive deletion |

A viewer or uploader with a perfectly valid Access JWT gets 403 on every
GitHub-touching endpoint (tested). An identity that is not in the allow-list
gets 403 on every API call. Bearer tokens (`pkgops serve token`) keep working
next to Access for CI and the CLI; they carry their own role.

## Setup, by hand

### 1. GitHub App (once)

Create a GitHub App owned by your organisation (Settings, Developer settings,
GitHub Apps, New). No webhook. Permissions, repository level only:

| permission | why |
|---|---|
| Contents: **Read and write** | read and commit `prune.yaml`; create archive releases and upload their assets |
| Actions: **Read and write** | `workflow_dispatch` (write) and run status (read) |
| Metadata: Read | mandatory |

Install the App on **the packages repository only**. Note the *App ID* and the
*installation ID* (the number in the installation's URL). Generate a private key
(a `.pem`) and store it **on the host that runs `pkgops serve`**, mode 0600,
owned by the service user. PkgOps refuses a key file that is group- or
world-accessible, reads the file when it needs a token (it is not cached), signs
a 9-minute RS256 JWT, exchanges it for an installation token and keeps that only in memory
until two minutes before it expires. The key is never taken from a flag value
and never logged.

Limits worth knowing: the installation token's rate limit is 5,000 requests per
hour (an overview costs two requests, plus a listing of the archive that is cached for two
minutes because it reads one manifest per archived version; PkgOps reports GitHub's reset
time when the limit is hit); a release asset may be at most **2 GB**, which PkgOps checks
before uploading.

A plain token (`$PKGOPS_GITHUB_TOKEN`, or the Actions job token
`$GITHUB_TOKEN`) is accepted as a fallback for development and tests. It is less
preferred: it is tied to a person or lives longer than an installation token.

### 2. Cloudflare Access (once)

Create a self-hosted Access application for the hostname of `pkgops serve` and a
policy that lists the people (and, for automation, a service token). Copy the
team name (`<team>.cloudflareaccess.com`) and the application's **AUD tag**.
`pkgops serve` verifies the `Cf-Access-Jwt-Assertion` header itself: RS256
signature against `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`
(cached, re-fetched when a new key id appears), issuer, audience and expiry. It
**never** trusts `Cf-Access-Authenticated-User-Email`. It refuses to start
unless `--access-team`, `--access-aud` and `--access-allow` are all given.

**The server must only be reachable through Access**: bind it to loopback and
reach it through a Cloudflare Tunnel, or firewall it to Cloudflare. The JWT
check is defence in depth; the static `/ui/` files are not behind it.

The allow-list (`access-allow.txt`, one `identity role` per line, case
insensitive; a service token is its `common_name`, `<client-id>.access`):

```
alice@example.com   admin
bob@example.com     viewer
```

### 3. Run it

```sh
export PKGOPS_GITHUB_APP_ID=123456 PKGOPS_GITHUB_INSTALLATION_ID=7654321
export PKGOPS_GITHUB_KEY_FILE=/usr/local/etc/pkgops/app.pem      # chmod 600
pkgops serve --ui --listen 127.0.0.1:39112 \
  --source https://pkg.example.org \
  --prune-via github:jailops/jailops-packages@master:prune.yaml \
  --workflow publish.yml --archive \
  --policy pkgops-repo.yaml \
  --access-team example --access-aud 0123abcd... --access-allow /usr/local/etc/pkgops/access-allow.txt
```

Without `--prune-via` the service still shows everything and cleanup requests
become manual steps. The web UI is off by default: add `--ui` to serve it (the admin UI needs it).

## The permanent archive (GitHub Releases)

Pages is the live repository; Releases hold **every published `.pkg` forever**.
The publish workflow runs `pkgops archive push --repo OWNER/REPO` after a
successful build:

- one release per package version, tag `pkg-<name>-<version>`, one asset per
  ABI (`hello-0.1.0-FreeBSD_15_amd64.pkg`) and an `archive.json` asset with the
  sha256 and size of each, repeated in the release notes. The layout keeps names
  unique and every version listable through the Releases API;
- idempotent: a version already archived with the same sha256 is skipped, a
  different sha256 for the same version is an error;
- restore: `pkgops fetch --from github-releases:OWNER/REPO --into site --prune
  prune.yaml` rebuilds the baseline from the archive (verified against the
  manifests), used automatically by `--source auto` when the site has no index;
- `pkgops verify-archive --repo OWNER/REPO --from https://pkg.example.org`
  lists what is on Pages but not archived, archived but neither on Pages nor
  pruned, sha256 mismatches, and what was pruned from Pages and is still (or no
  longer) archived.

Pruning never touches the archive. **Deleting from the archive is separate and
deliberate**: `pkgops archive prune --repo OWNER/REPO --abi A --name N --version V
--reason "..." --confirm "delete from archive"`, or the red dialog in the UI with
the same typed confirmation and a reason. The admin identity and the reason are
appended to the body of the `pkgops-archive-audit` release *before* anything is
deleted, and a release left without assets is removed with its tag.

## Unverified

Real GitHub (App token exchange, contents, releases, dispatch, runs, rate-limit
behaviour, the 2 GB limit) and real Cloudflare Access (certs endpoint format,
header name, AUD handling, tunnel setup) were not reachable; the code follows
their documented APIs and is tested against fakes. The publish workflow in
[hosting.md](hosting.md) has not run on GitHub.
