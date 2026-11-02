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

| action | where | owner |
|---|---|---|
| `actions/checkout`, `actions/setup-go`, `actions/setup-node`, `actions/cache` | `ci.yml`, `release.yml` | GitHub (first party) |
| `goreleaser/goreleaser-action@v6` | `release.yml`, with `contents: write` | third party |

First-party `actions/*` stay on their major tag (`@v7`). A tag can be moved by its owner; for GitHub's own
actions that is accepted here. The majors in the files were not checked against the newest releases when this was
written (no network): check them with `gh api repos/actions/checkout/releases/latest --jq .tag_name` and so on.

## Pinning a third-party action to a commit SHA

A tag such as `@v6` can be moved to other code at any time; a full commit SHA cannot. To pin, on a machine with the
GitHub CLI logged in:

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

and set `ACTIONS_PIN_ENFORCE: "1"` in `ci.yml`. From then on CI fails on any third-party action that is not pinned.

To update a pin later, read what changed before you move it:

```sh
gh api repos/goreleaser/goreleaser-action/compare/<old sha>...<new sha> --jq '.commits[].commit.message'
```

## The check

`scripts/check-actions-pinned.sh` reads `.github/` and `action.yml` and reports every `uses:` of a third-party
action that does not end in a 40-hex SHA. First party (`actions/`, `github/`), local (`./`) and `docker://` are
allowed. `ACTIONS_PIN_ENFORCE=1` makes it fail; otherwise it only warns. `ci.yml` runs it on every push and pull
request; the variable at the top of `ci.yml` is the only switch.

## For users of the pkgops action

`uses: jailops/jailops-pkgops@v0.1.0` in your workflow is a third-party action for you, and it receives your
signing key as an input. Pin it to the commit of the release tag
(`gh api repos/jailops/jailops-pkgops/commits/v0.1.0 --jq .sha`). With a SHA the action builds pkgops from that
commit instead of downloading the release binary (`scripts/install-pkgops.sh`).
