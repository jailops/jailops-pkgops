#!/usr/bin/env bash
# Cut a release straight on master (no PR): checks, version.txt, CHANGELOG.md, commit, annotated tag, push.
#
#   scripts/release.sh vX.Y.Z          (normally: just release vX.Y.Z)
#   DRY=1 scripts/release.sh vX.Y.Z    (just release-dry vX.Y.Z) runs every check, changes and pushes nothing
#
# The tag push starts .github/workflows/release.yml (GoReleaser -> GitHub release). Never run this from an
# agent session; a person runs it.
set -euo pipefail
cd "$(dirname "$0")/.."

DRY=${DRY:-0}
TAG=${1:-}
BRANCH=master
REMOTE=origin

die() { echo "release: $*" >&2; exit 1; }
say() { echo "==> $*"; }
act() { if [ "$DRY" = 1 ]; then echo "[dry-run] would: $*"; else say "$*"; fi; }

# --- the version ------------------------------------------------------------------------------------------
[ -n "$TAG" ] || die "usage: just release vX.Y.Z"
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'
[[ "$TAG" =~ $semver ]] || die "'$TAG' is not a semantic version tag like v0.1.0 or v0.2.0-rc.1"
VERSION=${TAG#v}
DATE=$(date -u +%Y-%m-%d)

# --- git state --------------------------------------------------------------------------------------------
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"
[ -z "$(git status --porcelain)" ] || die "working tree is not clean (commit or stash first)"
[ "$(git rev-parse --abbrev-ref HEAD)" = "$BRANCH" ] || die "not on $BRANCH (releases are cut from $BRANCH only)"
git remote get-url "$REMOTE" >/dev/null 2>&1 || die "no git remote '$REMOTE'"
git fetch --quiet --tags "$REMOTE" || die "cannot fetch $REMOTE"
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then die "tag $TAG already exists locally"; fi
if git ls-remote --exit-code --tags "$REMOTE" "refs/tags/$TAG" >/dev/null 2>&1; then die "tag $TAG already exists on $REMOTE"; fi
if git rev-parse -q --verify "refs/remotes/$REMOTE/$BRANCH" >/dev/null; then
  [ "$(git rev-list --count "HEAD..$REMOTE/$BRANCH")" = 0 ] || die "$BRANCH is behind $REMOTE/$BRANCH: git pull --ff-only first"
fi
say "git state ok: clean tree on $BRANCH, $TAG is new"

# --- the changelog must have something to release ---------------------------------------------------------
[ -f CHANGELOG.md ] || die "CHANGELOG.md is missing"
grep -q '^## \[Unreleased\]' CHANGELOG.md || die "CHANGELOG.md has no '## [Unreleased]' section"
if grep -q "^## \[$VERSION\]" CHANGELOG.md; then die "CHANGELOG.md already has a section for $VERSION"; fi
body=$(awk '/^## \[Unreleased\]/{f=1;next} /^## /{f=0} f' CHANGELOG.md | tr -d '[:space:]')
[ -n "$body" ] || die "the Unreleased section of CHANGELOG.md is empty"
say "changelog ok"

# --- checks -----------------------------------------------------------------------------------------------
say "gofmt"
bad=$(gofmt -l . | grep -v '^web/node_modules/' || true)
[ -z "$bad" ] || { echo "$bad" >&2; die "gofmt: unformatted files above"; }
say "go vet"
go vet ./...
say "go test -race ./..."
go test -race -count=1 ./...

# The embedded web UI (internal/webui/dist) is committed, so a release needs no Node. When Node is here,
# prove the committed dist matches web/; either way prove a clean checkout builds.
if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
  say "web: typecheck, unit tests, build, and the committed dist is up to date"
  (cd web && npm ci --no-audit --no-fund && npm run typecheck && npm test && npm run build)
  [ -z "$(git status --porcelain -- internal/webui/dist)" ] || {
    git status --short -- internal/webui/dist >&2
    die "internal/webui/dist is out of date with web/: commit the result of 'just web' first"
  }
else
  echo "WARNING: node/npm not found: the web UI tests and the dist up-to-date check were SKIPPED" >&2
fi
say "a clean checkout builds without Node (git archive HEAD, go build)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git archive HEAD | tar -x -C "$tmp"
(cd "$tmp" && CGO_ENABLED=0 go build -trimpath -o /dev/null ./cmd/pkgops)
say "all checks passed"

# --- the release itself -----------------------------------------------------------------------------------
act "write version.txt = $VERSION"
act "CHANGELOG.md: move Unreleased under '## [$VERSION] - $DATE'"
act "git commit -m 'Release $TAG' (version.txt, CHANGELOG.md)"
act "git tag -a $TAG -m 'pkgops $TAG'"
act "git push --atomic $REMOTE $BRANCH refs/tags/$TAG   (the tag starts the Release workflow)"
if [ "$DRY" = 1 ]; then
  echo "[dry-run] nothing was changed or pushed"
  exit 0
fi

printf '%s\n' "$VERSION" > version.txt
awk -v v="$VERSION" -v d="$DATE" '
  /^## \[Unreleased\]/ && !done { print; print ""; print "## [" v "] - " d; done=1; next }
  { print }' CHANGELOG.md > CHANGELOG.md.new
mv CHANGELOG.md.new CHANGELOG.md

git add version.txt CHANGELOG.md
git commit -q -m "Release $TAG"
git tag -a "$TAG" -m "pkgops $TAG"
if ! git push --atomic "$REMOTE" "$BRANCH" "refs/tags/$TAG"; then
  echo "release: push failed. Local commit and tag $TAG exist. Fix the cause, then:" >&2
  echo "  git push --atomic $REMOTE $BRANCH refs/tags/$TAG" >&2
  echo "or undo:  git tag -d $TAG && git reset --hard HEAD~1" >&2
  exit 1
fi
say "released $TAG: watch https://github.com/jailops/pkgops/actions (workflow: release)"
