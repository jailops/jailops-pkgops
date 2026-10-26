# FreeBSD .pkg and repository formats, as pkgops implements them

Everything here was derived from the pkg-create(8), pkg-repo(8), pkg-repository(5)
and pkg-script(5) manual pages and from inspecting packages and catalogs on a real
FreeBSD 15.1-RELEASE arm64 host with pkg 2.8.4 (`pkg create`, `pkg repo`, packages
from the official repositories). "Verified" means a real `pkg` accepted or
produced the thing; see the end for what is not verified.

## The .pkg file

A `.pkg` is a **zstd-compressed tar archive** (`packing_format "tzst"`, the
default). Entry order and headers as written by FreeBSD:

1. `+COMPACT_MANIFEST` - JSON, the metadata only.
2. `+MANIFEST` - the same JSON plus `files`, `config`, `directories`, `scripts`.
3. Payload files, with absolute paths (`/usr/local/bin/hello`), sorted by path.
4. Directories owned by the package, with a trailing slash (`/var/db/hello/`).

Manifest entries have mode 0644, `root:wheel`, mtime 0. Payload entries carry the
real mode and the **user and group names** (pkg resolves names at install time),
mtime = the build timestamp.

### Manifest JSON

Key order written by pkg (pkgops copies it, though pkg does not require it):

```
name origin version comment maintainer www abi arch prefix flatsize
licenselogic licenses desc deps categories users groups
files config directories scripts
```

- `abi`: `FreeBSD:15:amd64` / `FreeBSD:15:aarch64`. `arch`: the legacy form
  `freebsd:15:aarch64:64`. Both are derived from the ABI.
- `flatsize`: sum of the sizes of regular files (directories do not count).
- `files`: `path -> {sum, uname, gname, perm, mtime}`. `perm` is a 4-digit octal
  string (`"0755"`, `"04555"` for setuid). `sum` is `"1$<sha256 hex>"`. FreeBSD's
  own `pkg create` writes `"2$<base32 blake2b>"` for ports and `"1$..."` for base
  packages; pkg accepts both, and pkgops uses the `1$` form so it only needs
  SHA-256 (verified: install, `pkg check -s`, tampered package rejected).
- `config`: list of file paths pkg treats as configuration (`@config`).
- `directories`: `path -> {uname, gname, perm}`; pkg removes them on deinstall
  when empty.
- `deps`: `name -> {origin, version}`. **pkg skips ("malformed dependency")
  dependencies without origin or version**, so pkgops requires both.
- `users` / `groups`: only *names*. pkg does not create them from this list;
  ports put `pw` commands into the pre-install script, and pkgops does the same.
- `scripts`: `pre-install`, `post-install`, `pre-deinstall`, `post-deinstall`
  (pkg-script(5)). `lua_scripts` also exist; pkgops does not write them.

### Symlinks

A symlink is a tar entry of type symlink (size 0, mode 0755, `Linkname` = target)
and a `files` entry with an extra `symlink_target` key, placed between `perm` and
`mtime`: `{"sum":...,"uname":"root","gname":"wheel","perm":"0755","symlink_target":"real","mtime":...}`.
`flatsize` counts the length of each target. The checksum is the SHA-256 of the
link target, with one quirk found by checking packages FreeBSD builds itself:
for an **absolute** target pkg hashes the target *without its leading slash and
including the terminating NUL byte* (`usr/share/misc/termcap\0`); relative
targets are hashed as written. With the wrong rule `pkg check -s` reports a
checksum mismatch for the link (everything else still installs), so pkgops
follows the rule and a unit test pins it to values from FreeBSD-runtime-15.1.
Verified: `pkg add`, `pkg check -s` (also detects a retargeted link),
`pkg delete`, and `pkg repo` ingestion, for relative, absolute, dangling and
directory links. Hardlinks are not supported.

### Fields pkg's database requires

`pkg update` stores each catalog entry in sqlite with `NOT NULL` columns. A
package lacking **`desc`, `maintainer` or `www`** makes the *whole* repository
update fail on the client (`NOT NULL constraint failed: packages.desc`). pkgops
therefore requires `maintainer` and `www`, defaults `desc` to `comment`, and
always writes `licenselogic`. (Found the hard way; verified on the host.)

### @config semantics

A file listed in `config` is installed at its real path when absent. On
upgrade: if the file is unmodified it is replaced silently; if the admin
changed it, the local file is kept and the new version is written beside it as
`<file>.pkgnew` (pkg also prints "Unable to merge configuration file"). On
deinstall, unmodified config files are removed. All three verified. pkgops does
**not** implement the ports `@sample` convention (install `foo.conf.sample`,
copy to `foo.conf` in a script); `config: true` is simpler and is native pkg.

## Repository layout

pkg.FreeBSD.org uses `<ABI>/<set>/` (`FreeBSD:15:amd64/latest/`) holding `All/`
plus catalogs. pkgops uses the same, so the client URL is
`https://host/${ABI}/latest`:

```
<root>/FreeBSD:15:aarch64/latest/
    All/hello-0.1.0.pkg
    meta.conf  meta           repository descriptor (two identical copies)
    data.pkg                  catalog (primary, JSON)
    packagesite.pkg           catalog (deprecated, kept for older clients)
    files.pkg                 optional file index (pkgops repo --files-list)
```

Catalog entries refer to packages by `path` relative to the repository URL
(`All/hello-0.1.0.pkg`); `pkg repo` run on a flat directory writes
`hello-0.1.0.pkg` instead, otherwise the records are identical.

### meta.conf

Byte-identical to what `pkg repo` writes (checked in a test):

```
version = 2;
packing_format = "tzst";
manifests = "packagesite.yaml";
data = "data";
filesite = "files";
manifests_archive = "packagesite";
filesite_archive = "files";
```

### data.pkg and packagesite.pkg

Each is a zstd tar. `packagesite.pkg` holds `packagesite.yaml`: one compact JSON
manifest per line (newline-terminated). `data.pkg` holds `data`:
`{"groups":[],"expired_packages":[],"packages":[ ... ]}`.

A catalog record is the compact manifest with extra keys inserted where pkg puts
them: `sum` after `prefix`, `path` and `repopath` after `flatsize`, `pkgsize`
before `desc`. `sum` is a checksum of the `.pkg` file (`1$<sha256>` from pkgops;
`2$<blake2>` from `pkg repo`), `pkgsize` its size in bytes.

**Verified by diff**: for the same `.pkg`, pkgops' `data` and `packagesite.yaml`
equal the ones `pkg repo` produced on FreeBSD, byte for byte, except the `sum`
value and the `All/` path prefix.

pkg's own `pkg repo` keeps *every* version it finds in the catalog; pkgops
advertises only the newest version of each name (older files stay in `All/`
until pruned). Clients upgrade identically (verified: 0.1.0 -> 0.1.1).

### files.pkg

`files` inside the archive: a front-coded directory dictionary (`N suffix`,
N = length of the common prefix with the previous directory), an empty line,
then one block per package (`name version`, `>dirindex`, basenames, empty line).
pkgops output equals `pkg repo -l` for the same package (test) and `pkg rwhich`
works against it (verified).

## Signing

pkg-repo(8) signs "a hash of the repository": SHA-256 of the **uncompressed
catalog file** (`data`, `packagesite.yaml`, `files`), written as lowercase hex;
that 64-character *string* is then signed with RSA PKCS#1 v1.5 / SHA-256 (the
same as `printf %s $hex | openssl dgst -sha256 -sign key`). Verified by checking
a FreeBSD-signed catalog with openssl.

Two ways to carry the signature inside each catalog tar:

| client `signature_type` | tar entries before the payload | pkgops flag |
|---|---|---|
| `pubkey` (+ `pubkey: path`) | `signature` = 256 signature bytes **plus one NUL byte** | `--sign pubkey` (default) |
| `fingerprints` (+ `fingerprints: dir`) | `<payload>.sig` (raw signature) and `<payload>.pub` (PEM public key) | `--sign fingerprints` |

For `fingerprints` the client trusts a key if `dir/trusted/<any file>` contains
`function: sha256` / `fingerprint: "<sha256 of the .pub PEM bytes>"`
(`pkgops key fingerprint repo.pub` prints it). Signature entries have mtime 0.

Verified with pkg 2.8.4: good signature installs; repo signed by another key,
or a catalog altered after signing, is rejected (`signature verification
failure`, repository removed); both modes. ECDSA and EdDSA signers exist in pkg
and are not implemented.

Notes on `pkg -r <root>`: the client resolves `fingerprints` relative to
the host, not to the root, in confusing ways; test fingerprints without `-r`.

## Client facts worth knowing

- A client caches `name-version~hash.pkg` and validates it; republishing the
  same version with different bytes does not reach clients that already cached
  it. pkgops refuses that unless `--replace` is given.
- A package built for `FreeBSD:15:amd64` is filtered by ABI by the client
  (`pkg -o ABI=...`); keep each ABI in its own directory.
- The `pkg+https` scheme is for DNS SRV mirrors; use plain `https://`.

## Generated services (`service:`)

For a Go daemon that runs in the foreground, `pkgops.yaml` can say what to run
and pkgops writes the rc.d script. The hand-written `services:` list (a script
from a file) is unchanged and can be used in the same manifest for other names.

```yaml
service:
  name: myservice                     # rc name; default: the package name
  command: /usr/local/bin/myservice   # absolute, or relative to prefix
  user: myservice
  restart: true
```

| key | meaning |
|---|---|
| `name` | lowercase letters, digits, `_` (no `-`: rc.subr builds variables like `<name>_enable`). Default: package name. |
| `command` | the foreground program. Letters, digits and `._+@%,=:/-` only. |
| `args` | default arguments (list; any text, quoted safely). |
| `user` | the user it runs as (`daemon -u`). Default: the service name. `root` runs as root. |
| `restart` | supervise with `daemon -r`: restart after any exit, 1 s delay. |
| `restart_delay` | seconds between restarts (`daemon -R`); needs `restart: true`. |
| `env` | environment variables exported before start (any text, quoted safely). |
| `log.file` | append stdout and stderr to this file (`daemon -o`, mode 0640, reopened on SIGHUP). |
| `log.syslog` | send output to syslog, tag = service name (`daemon -S -T`). Both can be used. |
| `log.rotate` | with `log.file`: install `etc/newsyslog.conf.d/<name>.conf` (daily, keep N compressed archives, SIGHUP to the supervisor). Config file: edits survive upgrades. |
| `require`, `before` | rc keywords for `# REQUIRE:` / `# BEFORE:`. Default require: `DAEMON` (which implies NETWORKING and syslogd order). |

Unknown keys are errors. The section is one mapping today; because it carries its own
`name`, a list form for several services per package can be added without
breaking this one.

What the package gets: `etc/rc.d/<name>` (mode 0555, like `services:`), the user
(and a group of the same name) in the pre-install script unless `user` is `root`
or already in `users:`, the newsyslog fragment, and a pre-deinstall step that
runs `service <name> onestop` before files are removed (an install over a
running older version with `pkg add` left the old process running: restart
the service after an upgrade; pkg upgrade itself was not tried). A generated user has no login shell and **no home**
(`/nonexistent`); declare it in `users:` for a home, and use `dirs:` for state
directories (`/var/db/<name>`). Nothing is enabled: `sysrc <name>_enable=YES`.
The user is **not** removed on `pkg delete`; log files and archives are left
too (`/var/log/<name>.log*`), as the package does not own them.

### The generated script

For the example above (the header comments are shortened):

```sh
#!/bin/sh
# PROVIDE: myservice
# REQUIRE: DAEMON
# KEYWORD: shutdown

. /etc/rc.subr

name=myservice
rcvar=myservice_enable
desc='...package comment...'

load_rc_config $name

: ${myservice_enable:=NO}
: ${myservice_runas:=myservice}
: ${myservice_args=''}

pidfile=/var/run/myservice.pid
procname=/usr/sbin/daemon
command=/usr/sbin/daemon
command_args="-f -r -P /var/run/myservice.pid -p /var/run/myservice.child.pid -u ${myservice_runas} /usr/local/bin/myservice ${myservice_args} ${myservice_flags}"
start_precmd=myservice_prestart

myservice_prestart()
{
	# myservice_flags are program arguments (see command_args), not daemon(8) options
	rc_flags=
	if [ ! -x /usr/local/bin/myservice ]; then
		warn "cannot run /usr/local/bin/myservice"
		return 1
	fi
	return 0
}

run_rc_command "$1"
```

Why it looks like this (from daemon(8) and rc.subr(8) on 15.1):

- `daemon -P` writes the **supervisor** pid and `-p` the program's. rc.subr's
  `pidfile` is the supervisor's, with `procname` `/usr/sbin/daemon` (daemon sets
  its title to `daemon: <command>[pid]`, which `check_pidfile` matches).
  `service stop` therefore signals the supervisor, which forwards SIGTERM to the
  program, waits for it, removes both pid files and exits; nothing restarts the
  program. daemon(8) warns that signalling the program itself, the child pid
  file, would only make `-r` restart it. Without `restart` there is no `-r`
  but the same pid files are kept, so `status`/`stop` work identically.
- `-f` closes stdin/stdout/stderr first; `-o`/`-S` then redirect output.
- Settings in `/etc/rc.conf` (all optional): `<name>_enable`, `<name>_runas`
  (the user; **not** `<name>_user`, which rc.subr would use to run daemon
  itself through su), `<name>_args` (replaces the packaged `args`),
  `<name>_flags` (extra arguments after them), and the native `<name>_env` and
  `<name>_chdir`. Variables hold words that the shell splits and quotes once.
- Injection safety: names, paths and rc keywords must match strict patterns;
  `args` and `env` values may be any text without control characters (newline,
  NUL), and are single-quoted (`'` becomes `'\''`) before they enter the script.
  Tests round-trip hostile values through `sh`.

### Verified on a real system

FreeBSD 15.1 (arm64 and amd64), package built by pkgops, `pkg add`, user
`pkgopstest` created, `service` run as root:

- disabled by default (`Cannot 'start'`), `sysrc x_enable=YES`, `service -e` lists it,
  `rcorder` puts it after `DAEMON`, `rcorder -k shutdown` lists it;
- `restart: true`: `kill -9` of the program: the supervisor pid stays, a new
  program pid appears after about 1 s (about 5 s with `restart_delay: 5`), and
  `status` stays "running as pid <supervisor>"; `service restart` and `stop`
  leave no process and no pid file for 6 s of watching; `start` works again;
- `restart: false`: `kill -9` of the program ends it, pid files are removed,
  `status` says not running, no restart;
- the program runs as the service user, log file mode 0640, `daemon -H`
  reopens it on SIGHUP, `newsyslog -F` rotates it and accepts the fragment;
  `-S` logs to `/var/log/messages` with the tag;
- hostile `args` and `env` values (`;`, `$(...)`, quotes) arrive as plain text;
- `<name>_args` and `<name>_flags` in rc.conf override as described;
- `pkg delete` of a running service stops it (supervisor and program), removes
  script and pid files. Left behind: the user, its group, log files, the
  `rc.conf` line.

Not verified: a real reboot (boot start and shutdown stop), capsicum/jails
(`service` jails and `<name>_svcj` were not tried), non-root `service status`
(the pid files are root-only, mode 0600), services with many restarts per second
(a crashing program is restarted forever, every `restart_delay` seconds).

Left out on purpose, for hand-written scripts or a port: several services,
`required_files`, `stop_timeout`/kill escalation, runtime directories created
at start, working directory (`<name>_chdir` works), limits, `provide`, other
pid file locations, restart counts (`daemon -C`), syslog facility and priority.

## The Go toolchain that builds the package

`pkgops build` cross-compiles with `GOTOOLCHAIN=auto` unless the environment
already sets `GOTOOLCHAIN` (an explicit value from you or CI is respected), so
the `go` and `toolchain` lines of your `go.mod` select, and if needed download,
the Go that builds your program, whatever older Go the build machine ships. The
version actually used is printed (`building with go1.25.3`). The Go version is
part of the build identity: the same inputs built by the same Go give the same
bytes. The binaries are static (`CGO_ENABLED=0`), so an installed package never
depends on a Go installation or any Go package on the FreeBSD host.

## Minimum release: how pkg enforces the OS version

The ABI (`FreeBSD:15:amd64`) names the major version only. pkg(8) itself checks
a package's minimum **release** through the manifest annotation
`FreeBSD_version`, the `__FreeBSD_version` that `uname -U` prints (1501000 on
15.1-RELEASE; minor releases are `major*100000 + minor*1000`). PkgOps writes it
from `min_release: "15.1"` (or `min_osversion: 1501000`) in `pkgops.yaml`, into
the manifest and the repository catalog. Verified with pkg 2.8.4 on a real
FreeBSD 15.1 host (`uname -U` = 1501000):

| package minimum | running | what pkg did |
|---|---|---|
| 15.1 (1501000) | 15.1 | installs |
| 15.0 (1500000) | 15.1 | installs |
| 15.2 (1502000) | 15.1 | `pkg update` fails: "Newer FreeBSD version for package hello: - package: 1502000 - running userland: 1501000. Ignore the mismatch and continue? [y/N]" followed by "repository contains packages for wrong OS version"; `pkg add` of the file refuses the same way |

So pkg enforces a minimum **minor** release, but note: (a) the check runs when the
catalog is read, so **one package that needs a newer release makes `pkg update`
fail for the whole repository on an older host**; (b) it is a question with a
default of "no": `pkg -y`, `ASSUME_ALWAYS_YES` or `IGNORE_OSVERSION=yes`
override it; (c) a 15.0 host (`uname -U` 15000xx) is refused by a package whose
minimum is 15.1, which is the point; this specific 15.0 case was not run (no 15.0
host), the 15.2-on-15.1 case exercises the same comparison.

## The published-file index, `pkgops-index.json`

The pkg catalog advertises only the newest version of each package. The index
lists **every** published `.pkg` of every ABI, so a later build can restore the
whole history and the admin UI can show it. It sits in the repository root
(next to `FreeBSD:15:amd64/`), is written atomically after the catalogs, and is
versioned (`schema`, currently 1; a reader refuses a newer one).

```json
{ "schema": 1, "generator": "pkgops", "generated": "2026-10-26T11:00:00Z",
  "packages": [ { "abi": "FreeBSD:15:amd64", "name": "hello", "version": "0.1.0",
      "file": "FreeBSD:15:amd64/latest/All/hello-0.1.0.pkg", "sha256": "...", "size": 2004173,
      "built": "2026-10-26T11:00:00Z", "min_release": "15.1", "min_osversion": 1501000,
      "comment": "..." } ],
  "pruned": [ { "abi": "...", "name": "hello", "version": "0.0.9", "sha256": "...", "size": 1,
      "built": "...", "reason": "superseded", "pruned_at": "..." } ] }
```

`built` is when the file was first indexed. A file in `packages` that is missing
or has another sha256 when the index is next written **fails the run** (restore
it with `pkgops fetch`, or list it in the prune manifest); nothing is dropped
silently. A name-version that appears under `pruned` is never accepted again.
Crash behaviour: catalogs first, index last, and a file is removed from disk only
after both stopped listing it; a leftover file of an interrupted prune is
removed on the next run, never re-published.

## The prune manifest, `prune.yaml`

A committed list of published files the next build leaves out. It is a plan, not
an action:

```yaml
schema: 1
entries:
  - abi: FreeBSD:15:amd64
    name: hello
    version: "0.1.0"
    reason: superseded            # one line
    force: false                  # true is needed to prune the newest version of a package
    requested_by: alice@example.com
    requested: "2026-10-26T11:00:00Z"
```

`pkgops repo --prune prune.yaml` and `pkgops fetch --prune prune.yaml` honour it:
listed files are not fetched, are removed from the repository directory, and are
absent from the catalog and the index, which records them under `pruned`.
Anything not listed is untouched. An entry for the newest version of its package
is refused unless it says `force: true`. `pkgops prune --plan` (or the admin UI)
writes the entries; it never deletes. The archive in GitHub Releases is not
touched by a prune.

## The repository policy, `pkgops-repo.yaml`

```yaml
schema: 1
abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]   # packages for other ABIs are refused
min_release: "15.1"                            # packages must declare at least this (min_release in pkgops.yaml)
retention:
  keep: 5                                      # a hint for `pkgops prune --plan`; nothing is deleted automatically
```

Honoured by `pkgops repo` (a new package for another ABI, or with a lower or
missing minimum release, is refused with the reason and the fix), `pkgops publish`
(refuses to publish a repository whose index holds such a package) and
`pkgops serve` (uploads). Existing history below a raised minimum is not
re-checked by `repo`; `publish` does, so prune it first.

## Not verified

See README "Status". In short: amd64 targets on real amd64 hardware, `service
start` of the rc.d script on a real system (the script was sourced via
`rc.subr` with `rcvar`, and the binary was run natively), rsync publishing, the
GitHub Action on GitHub, ECDSA/EdDSA, pkg versions other than 2.8.4.
