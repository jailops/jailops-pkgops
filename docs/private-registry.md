# A private package registry for your own machines

A pkg repository that only your machines can read: HTTPS with **mutual TLS**
(one client certificate per machine, issued by your own CA, individually
revocable) plus the normal **repository signature**. Plain `pkg update`,
`pkg install` and `pkg upgrade` keep working; the client needs no special
program, only three files and a repo config.

Scope: no VPN, no authentication proxy, no identity provider, no database. A
CA directory, a few PEM files and one config line per client.

Status: everything below marked **verified** was run with real pkg 2.8.4 on
FreeBSD 15.1 (arm64) against a pkgops binary built for FreeBSD, nginx 1.30.5
and private temporary directories. Anything not run is marked **UNVERIFIED**.

## Architecture

```
  FreeBSD machines                      the registry host (e.g. a VNET jail)
  ----------------                      ------------------------------------------
  pkg update/install                    nginx  (TLS 1.2+, ssl_verify_client on, ssl_crl)
    |  repos/private.conf                  |   or: pkgops serve --tls-cert .. --client-ca ..
    |  env: client cert + key + CA         v
    +--- HTTPS + client certificate ---> signed repository tree  <ABI>/latest/{meta.conf,data.pkg,All/*.pkg}
                                                ^
   admin machine (CA offline)                   | pkgops repo --key repo.key   (signing key, separate)
   pkgops pki ...  ---- crl.pem, certs ---------+
```

What each layer protects:

| Layer | Answers | Protects against | Does not protect against |
|---|---|---|---|
| mTLS (your CA) | who may connect | outsiders reading your packages, scanners, a leaked URL | a stolen client key of a machine that is not revoked yet |
| pkg signature (`repo.key`) | who made the repository | a tampered catalog or package, a compromised web server or mirror, a man in the middle | nothing about access: the signature is public information |
| upload token (`serve`) | who may write | unauthorised uploads and deletes | reading (that is mTLS) |

The layers are independent on purpose. The CA key and the repository signing
key are different keys; lose one and the other still holds. Verified live: with
a valid client certificate, a tampered `data.pkg` and a repository signed with
another key are both refused by pkg (see "What was verified").

Not suitable as a private registry:

- **GitHub Pages** from a private repository is not private: the published
  site is public (on most plans). Anything on Pages is readable by everyone.
- **Cloudflare Access** cannot protect a pkg repository: pkg (libfetch) cannot
  send the Access service-token headers or run a browser login.

## Step by step

### 1. Create the CA (on the admin machine, once)

```sh
pkgops pki init --dir ~/pkgops-ca --name "Example private CA"
```

Creates `ca.crt` (public), `ca.key` (mode 0400), `state.json` (serial counter,
list of issued certificates, revocations) and an initial `crl.pem`. ECDSA P-256
by default; `--key-type rsa [--rsa-bits 4096]` for RSA. CA validity defaults to
15 years (`--days`). It refuses to overwrite an existing CA.

The CA key signs every certificate and the CRL. Keep this directory **offline or
on the admin machine only**: never on the registry host, never on a client.
The servers only ever get `ca.crt` and `crl.pem`.

### 2. Issue the server certificate

```sh
pkgops pki issue server --dir ~/pkgops-ca --name pkg.example.lan,10.0.0.20
```

The first name is the common name; every name (DNS names and IP addresses) is
also a subject alternative name, and clients must connect using one of them.
EKU `serverAuth`, 1825 days (5 years) by default. Files: `servers/pkg.example.lan.crt`
and `.key` (key mode 0400).

### 3. Issue one client certificate per machine

```sh
pkgops pki issue client --dir ~/pkgops-ca --name web1
pkgops pki issue client --dir ~/pkgops-ca --name db1 --days 180
```

CN is the machine name, EKU `clientAuth` only (a server certificate cannot be
used as a client certificate and vice versa), 1825 days (5 years) by default, never longer
than the CA. Files: `clients/web1.crt` and `clients/web1.key` (mode 0400).
`pkgops pki issue` never prints a private key. It refuses to overwrite files.

### 4. Install the files on a client (as root)

```sh
mkdir -p /usr/local/etc/pkg/mtls /usr/local/etc/pkg/keys
install -m 0644 ca.crt          /usr/local/etc/pkg/mtls/ca.crt
install -m 0644 web1.crt        /usr/local/etc/pkg/mtls/client.crt
install -m 0400 -o root -g wheel web1.key /usr/local/etc/pkg/mtls/client.key
install -m 0644 repo.pub        /usr/local/etc/pkg/keys/private.pub

pkgops conf --name private --url 'https://pkg.example.lan/${ABI}/latest' \
    --pubkey /usr/local/etc/pkg/keys/private.pub \
    --client-cert /usr/local/etc/pkg/mtls/client.crt \
    --client-key  /usr/local/etc/pkg/mtls/client.key \
    --ca-cert     /usr/local/etc/pkg/mtls/ca.crt \
    > /usr/local/etc/pkg/repos/private.conf
```

Resulting `/usr/local/etc/pkg/repos/private.conf`:

```
private: {
  url: "https://pkg.example.lan/${ABI}/latest",
  signature_type: "pubkey",
  pubkey: "/usr/local/etc/pkg/keys/private.pub",
  env: {
    SSL_CLIENT_CERT_FILE: "/usr/local/etc/pkg/mtls/client.crt",
    SSL_CLIENT_KEY_FILE: "/usr/local/etc/pkg/mtls/client.key",
    SSL_CA_CERT_FILE: "/usr/local/etc/pkg/mtls/ca.crt",
  },
  enabled: yes
}
```

pkg reads the repo `env` block into the fetcher's environment for that
repository only, and libfetch uses `SSL_CLIENT_CERT_FILE`,
`SSL_CLIENT_KEY_FILE` and `SSL_CA_CERT_FILE`. **Verified with pkg 2.8.4**: with
this block `pkg update` and `pkg install` succeed against an mTLS-only server
(pkgops and nginx); the same configuration without the client certificate is
refused. No global `PKG_ENV` is needed. pkg runs as root, so the key is
`root:wheel 0400`.

Check on the client: `pkg update -f && pkg search mymarker`.

How a refusal looks (pkg 2.8.4 is not helpful here): with a missing, wrong-CA
or revoked certificate, **`pkg update` prints "repository is up to date" and
exits 0** although it fetched nothing, and a following `pkg install` fails with
"Repository NAME cannot be opened. 'pkg update' required" (exit 1). Use
`pkg -d update` to see the failed fetch, and the server log for the reason.
nginx answers a missing or revoked certificate with `400 Bad Request` that pkg
prints per file.

### 5. Revoke a machine (lost, stolen, retired)

```sh
pkgops pki revoke --dir ~/pkgops-ca --name web1      # or --serial 3 (pkgops pki list shows serials)
scp ~/pkgops-ca/crl.pem root@pkg.example.lan:/usr/local/etc/pkgops/crl.pem.new \
  && ssh root@pkg.example.lan mv /usr/local/etc/pkgops/crl.pem.new /usr/local/etc/pkgops/crl.pem
```

`revoke` records the revocation in `state.json` and regenerates `crl.pem`
(`--name` revokes all active client certificates of that name). Always copy the
CRL to a temporary name and `mv` it so a server never sees a half-written file.

- `pkgops serve` re-checks the CRL on its own; **no restart, no reload**.
- nginx reads `ssl_crl` at start and reload only: `nginx -s reload` (or
  `service nginx reload`). Verified: before the reload the revoked machine was
  still accepted by nginx, after the reload it was refused.

`pkgops pki crl --dir DIR [--days 365]` regenerates the CRL without a new
revocation. A new revocation always writes a fresh CRL at once, so the validity (default 1 year) only says how long
an unchanged list is accepted. A CRL has a `nextUpdate`; regenerate and copy it **before it
expires**: after `nextUpdate` plus the grace (24 h, `--crl-grace`) pkgops
**denies every client** (fail closed); nginx with `ssl_crl` is expected to refuse
all clients too once the CRL has expired (OpenSSL behaviour, UNVERIFIED here). Put this in cron on the admin machine and
alert on it:

```sh
pkgops pki crl --dir ~/pkgops-ca --check      # exit 1 if missing, expired or < 7 days left
```

`pkgops pki list` shows the CRL status and warns when it is near expiry.
`pkgops pki crl` warns when you generate a CRL valid for less than 7 days.

### 6. Rotate expiring certificates

`pkgops pki list` marks certificates that expire within 30 days. To rotate a
machine without a gap:

```sh
pkgops pki issue client --dir ~/pkgops-ca --name web1 --out ~/pkgops-ca/clients/web1-2027
# install the new files on web1, run pkg update to prove it works, then
pkgops pki list --dir ~/pkgops-ca                  # note the OLD serial
pkgops pki revoke --dir ~/pkgops-ca --serial 3     # NOT --name: that would revoke the new one too
```

Rotate the server certificate the same way (`issue server`, then replace the
two files on the host). `pkgops serve` reloads `--tls-cert/--tls-key` itself
(checked at most every 15 s; a half-copied pair is ignored and the previous
certificate keeps serving); nginx needs a reload. Clients pin the **CA**, not
the server certificate, so a new server certificate needs no client change. The
CA itself lasts 15 years by default; replacing it means issuing everything again.

### 7. The repository signing key (separate from the CA)

```sh
pkgops key generate --out repo.key          # builder; keep secret
pkgops build -o dist && pkgops repo --repo site --key repo.key dist
```

`repo.pub` goes to the clients (step 4). The CA proves a machine may connect;
`repo.key` proves the packages are yours. Do not reuse one for the other; the
registry host does not need `repo.key` if you publish a pre-signed tree
(`pkgops publish`), and does not need the CA key ever. Only `pkgops serve` with
the upload API needs `repo.key`, to re-sign after an upload.

## Serving it

Pick one. In both, the repository directory must be the only thing served and
directory listings must stay off.

### A. `pkgops serve` itself

```sh
pkgops serve --repo /var/pkg --listen 10.0.0.20:443 \
    --tls-cert /usr/local/etc/pkgops/server.crt --tls-key /usr/local/etc/pkgops/server.key \
    --client-ca /usr/local/etc/pkgops/ca.crt --crl /usr/local/etc/pkgops/crl.pem
```

- TLS 1.2 minimum, TLS 1.3 negotiated with every client that supports it.
- `--client-ca` makes a client certificate **required for everything**: the
  repository tree, `/api/v1/*`, the status and the web UI. Unauthenticated
  connections never reach a handler.
- It refuses to start with `--client-ca` and no `--tls-cert/--tls-key`, with
  `--crl` and no `--client-ca`, and when the CRL file (if given) is missing,
  unreadable or not signed by the CA. Without `--crl` there is no revocation:
  only use that for a first test.
- `--tls-cert/--tls-key` are reloaded when the files change (checked at most
  every 15 s). The client CA file and the CRL are re-checked at most every
  **5 s**.
- Denied clients are logged at info level (`client denied ... reason=...`
  with the certificate's CN and serial; never keys, tokens or headers).
- Uploads: the **bearer-token API still authorises writes**. mTLS identifies
  and admits the machine, the token says who may upload or delete; both are
  needed. `pkgops push` and `pkgops registry` take `--client-cert --client-key
  [--ca-cert]` (or `$PKGOPS_CLIENT_CERT`, `$PKGOPS_CLIENT_KEY`,
  `$PKGOPS_CA_CERT`). Verified: a push without a certificate fails in the TLS
  handshake; with certificate and token it is accepted.

How fast revocation takes effect, and what it covers:

| Case | Behaviour |
|---|---|
| New connection | the CRL file is re-checked (one `stat`) at most every 5 s, and re-read when its inode, size or mtime changed. **Maximum delay between the new `crl.pem` being in place and the first rejected connection: 5 s** (measured live: under 1 s when the previous check was older). |
| Resumed TLS session | the check runs in `tls.Config.VerifyConnection`, which also runs on resumption; a ticket cannot be used to skip it. Tested: a session that resumed before the revocation is refused after it. |
| Open keep-alive connection | the certificate is checked again on every HTTP request, so the next request is refused with 403 and the connection closed. A download that is already running is not cut. Idle connections are closed after 2 minutes. |
| CRL missing, unparsable, not signed by the CA, or expired beyond `--crl-grace` (24 h) | **all clients denied**, logged; the previous list is not kept. A good file restores access without a restart. |
| Client CA file broken | all clients denied until it is valid again. |

### B. nginx in front (static files, or proxying `pkgops serve`)

```nginx
server {
    listen 443 ssl;
    server_name pkg.example.lan;

    ssl_certificate         /usr/local/etc/nginx/pkg/server.crt;
    ssl_certificate_key     /usr/local/etc/nginx/pkg/server.key;
    ssl_protocols           TLSv1.2 TLSv1.3;

    ssl_client_certificate  /usr/local/etc/nginx/pkg/ca.crt;     # the CA that issues clients
    ssl_verify_client       on;                                  # required, not optional
    ssl_crl                 /usr/local/etc/nginx/pkg/crl.pem;    # re-read on reload only
    ssl_verify_depth        1;

    autoindex off;
    root /var/pkg;                                               # the repository directory only
    location / { try_files $uri =404; }
    location ~ /All/.*\.pkg$ { add_header Cache-Control "public, max-age=31536000, immutable"; }
}
```

Verified: pkg 2.8.4 installed through a private nginx 1.30.5 on a high port
with `ssl_client_certificate`, `ssl_verify_client on`, `ssl_crl`,
`ssl_protocols TLSv1.2 TLSv1.3`, `autoindex off` and a static `root`; a missing
or revoked certificate got `400`, and the revoked one only after
`nginx -s reload`. The `ssl_verify_depth 1` line and the `Cache-Control`
location are additions that were NOT part of that test (UNVERIFIED; run
`nginx -t` on your file).

To let nginx do mTLS and `pkgops serve` serve the tree and the API (UI,
uploads), run pkgops on a **loopback address or a unix socket only**:

```sh
pkgops serve --repo /var/pkg --listen unix:/var/run/pkgops/pkgops.sock --socket-mode 0660 --token-file tokens --key repo.key
```

```nginx
    location / {
        # nginx is the only client of the socket; do not forward client-supplied identity
        proxy_set_header X-Client-DN $ssl_client_s_dn;            # for logs only; overwrites any client value
        proxy_set_header X-Client-Verify $ssl_client_verify;
        proxy_pass http://unix:/var/run/pkgops/pkgops.sock:;
    }
```

(verified with pkg: valid certificate installs, no certificate refused.)

- `--listen unix:PATH` creates the socket with `--socket-mode` (default 0660;
  use the group of the nginx user, or 0600 and the same user), replaces a stale
  socket file of a crashed run, and refuses a path that is a regular file or a
  socket in use. On a TCP address use `127.0.0.1:PORT`. Do not give a
  proxied `pkgops serve` a non-loopback address: then it is reachable
  without mTLS.
- **pkgops trusts no identity header.** It never derives who a client is, or
  what it may do, from `X-Client-*`, `X-Forwarded-*`, `X-Remote-User` or any
  similar header; writes need the bearer token, and tests prove that such
  headers change nothing. If nginx forwards the client DN for logging, set it
  with `proxy_set_header` (which overwrites whatever the client sent) and
  make sure no other path reaches the socket; headers are not a security
  boundary, the socket permissions and nginx's `ssl_verify_client on` are.
- With nginx in front, `--client-ca` is not used on `pkgops serve` (it must
  stay plain HTTP on the socket); do not combine them.

## JailOps example (illustrative, not a tested spec)

This is only a sketch of how the registry could live in a VNET jail with its own
address, the repository as a volume and nginx inside. Names and keys are
schematic: adapt them to your JailOps version, and check the real spec format.

```yaml
# EXAMPLE ONLY - field names are illustrative
jails:
  - name: pkgreg
    vnet: true
    address: 10.0.0.20/24            # the address clients put in the URL and the server certificate
    packages: [nginx]
    volumes:
      - host: /tank/pkgrepo          # pkgops repo output (the signed tree), mounted read-only
        jail: /var/pkg
        readonly: true
      - host: /tank/pkgreg-tls       # server.crt, server.key, ca.crt, crl.pem (no CA key!)
        jail: /usr/local/etc/nginx/pkg
        readonly: true
    services: [nginx]
```

Updating the CRL then means replacing `crl.pem` in that volume and reloading
nginx in the jail. Keep the CA directory on the admin machine, outside every jail.

## What was verified (pkg 2.8.4, FreeBSD 15.1 arm64, pkgops serve with mTLS)

| Case | Result |
|---|---|
| (a) valid client certificate, `env:` block | `pkg update` and `pkg install` succeed |
| (b) no client certificate | refused (server: "client didn't provide a certificate"); `pkg update` says "up to date" with exit 0, `pkg install` fails |
| (c) certificate from another CA | refused (server: "signed by unknown authority"), same pkg behaviour |
| (d) certificate revoked, CRL file replaced while the server runs | refused on the next `pkg update`, no restart; log: `certificate revoked (serial 3)` |
| (e) valid certificate, tampered `data.pkg` | pkg: "No signature found in the repository", repository removed |
| (e) valid certificate, repository signed with another key | pkg: "signature verification failure ... Invalid signature, removing repository" |
| (f) nginx 1.30.5 on a high port, static root and a unix-socket proxy to `pkgops serve` | valid certificate installs through both; no certificate and revoked certificate refused; revocation needs `nginx -s reload` |

UNVERIFIED: the JailOps example (a schematic), pkg with RSA client
certificates (ECDSA P-256 was used), `fingerprints` signature mode over mTLS,
more than one CA in `--client-ca` (CRL handling is implemented, not run live),
a CA key on a hardware token.

## Not built

- Issuing and revoking certificates from the Web UI. Idea only: the CA key must
  stay off the registry host, so a UI would have to talk to an offline signer;
  until that is designed, use `pkgops pki` on the admin machine.
- Automatic CRL distribution to the servers (copy it as in step 5).
- OCSP, certificate transparency, encrypted CA keys (protect the CA directory
  with disk encryption or keep it on removable media).
