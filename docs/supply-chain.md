# CI supply chain

What runs in this repository's GitHub Actions, what it may do, and how third-party actions are pinned.

## Workflows and permissions

| workflow | job | permissions | secrets |
|---|---|---|---|
| `ci.yml` | `test`, `web` | `contents: read` | none |
| `release.yml` (tags `v*`) | `goreleaser` | `contents: write` (the GitHub release) | the default `GITHUB_TOKEN` only |

No workflow holds a signing key: release tags are signed on a maintainer's machine (`just release`), and the
release archives are verified by `checksums.txt`.

## Actions in use

| action | where | owner | pinned to |
|---|---|---|---|
| `actions/checkout`, `actions/setup-go` | `release.yml` | GitHub (first party) | commit SHA |
| `goreleaser/goreleaser-action` | `release.yml`, with `contents: write` | third party | commit SHA |
| `actions/checkout`, `actions/setup-go`, `actions/setup-node`, `actions/cache` | `ci.yml` | GitHub (first party) | major tag (`@v7`) |

The workflow that publishes pins every action to a commit SHA, first party ones too: a tag can be moved by its
owner, and `release.yml` is the one workflow with write access. `ci.yml` has read access only; its first party
actions stay on their major tag. GoReleaser itself is pinned to an exact version (`version:` in `release.yml`).

## Pinning an action to a commit SHA

A tag such as `@v6` can be moved to other code at any time; a full commit SHA cannot. To pin or to update a pin, on
a machine with the GitHub CLI logged in:

```sh
# 1. the commit the tag points at now (annotated tags are resolved to the commit)
gh api repos/goreleaser/goreleaser-action/commits/v6 --jq .sha
# 2. the exact version tags on that commit, for the comment (put the SHA from step 1 in)
gh api --paginate repos/goreleaser/goreleaser-action/tags --jq '.[] | select(.commit.sha == "SHA") | .name'
# 3. cross-check without the API
git ls-remote https://github.com/goreleaser/goreleaser-action 'refs/tags/v6' 'refs/tags/v6^{}'
```

Then in `.github/workflows/release.yml`:

```yaml
      - uses: goreleaser/goreleaser-action@<the 40-hex SHA from step 1>   # v6.x.y from step 2
```

`ACTIONS_PIN_ENFORCE` is `"1"` in `ci.yml`: CI fails on any third-party action, and on any action of `release.yml`,
that is not pinned.

To update a pin later, read what changed before you move it:

```sh
gh api repos/goreleaser/goreleaser-action/compare/<old sha>...<new sha> --jq '.commits[].commit.message'
```

## The check

`scripts/check-actions-pinned.sh` reads `.github/` and `action.yml` and reports every `uses:` of a third-party
action, and every `uses:` in `release.yml`, that does not end in a 40-hex SHA. First party (`actions/`, `github/`)
outside `release.yml`, local (`./`) and `docker://` are allowed. `ACTIONS_PIN_ENFORCE=1` makes it fail; otherwise
it only warns. `ci.yml` runs it on every push and pull request; the variable at the top of `ci.yml` is the only
switch.

## For users of the pkgops action

`uses: jailops/jailops-pkgops@v0.1.0` in your workflow is a third-party action for you, and it receives your
signing key as an input. Pin it to the commit of the release tag
(`gh api repos/jailops/jailops-pkgops/commits/v0.1.0 --jq .sha`). With a SHA the action builds pkgops from that
commit instead of downloading the release binary (`scripts/install-pkgops.sh`).
