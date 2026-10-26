#!/bin/sh
# Build FreeBSD packages with pkgops and publish them to GitHub Pages (a git
# branch) or GitHub Releases. Used by action.yml; runnable by hand.
#
# Environment:
#   PKGOPS            path of the pkgops binary                (default: pkgops)
#   PKGOPS_VERSION    release tag (v0.1.0): if pkgops is not on PATH, get that release's binary
#                     (sha256 verified) or build it, via scripts/install-pkgops.sh
#   PKGOPS_SPEC       manifest                                  (default: pkgops.yaml)
#   PKGOPS_ABIS       space/comma separated ABIs                (default: every ABI in the manifest)
#   PKGOPS_KEY_FILE   RSA private key (PEM), optional but strongly advised
#   PKGOPS_PUBLISH    pages | release | none                    (default: pages)
#   PKGOPS_KEEP       versions to keep per package, 0 = all     (default: 5)
#   PKGOPS_REPLACE    1 = overwrite an already published name-version (CI re-runs)
#   PKGOPS_REMOTE     git remote URL for pages                  (default: origin)
#   PKGOPS_BRANCH     pages branch                              (default: gh-pages)
#   PKGOPS_TAG        release tag for publish=release           (default: v<version>)
#   PKGOPS_WORKDIR    scratch directory                         (default: mktemp -d)
set -eu

PKGOPS=${PKGOPS:-pkgops}
if ! command -v "$PKGOPS" >/dev/null 2>&1 && [ -n "${PKGOPS_VERSION:-}" ]; then
  PKGOPS="$("$(dirname "$0")/install-pkgops.sh" | tail -n 1)/pkgops"
fi
SPEC=${PKGOPS_SPEC:-pkgops.yaml}
MODE=${PKGOPS_PUBLISH:-pages}
KEEP=${PKGOPS_KEEP:-5}
BRANCH=${PKGOPS_BRANCH:-gh-pages}
REMOTE=${PKGOPS_REMOTE:-origin}
WORK=${PKGOPS_WORKDIR:-$(mktemp -d)}
DIST=$WORK/dist
SITE=$WORK/site
mkdir -p "$DIST"

# Reproducible timestamps: the commit time of HEAD unless the caller chose one.
if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  SOURCE_DATE_EPOCH=$(git log -1 --format=%ct 2>/dev/null || date +%s)
  export SOURCE_DATE_EPOCH
fi

# One run builds every ABI (all of the manifest's `abis` unless PKGOPS_ABIS narrows it).
abis=$(printf '%s' "${PKGOPS_ABIS:-}" | tr ' ' ',')
if [ -z "$abis" ]; then
  "$PKGOPS" build -f "$SPEC" -o "$DIST"
else
  "$PKGOPS" build -f "$SPEC" --abi "$abis" -o "$DIST"
fi
pkgs=$(find "$DIST" -name '*.pkg' | sort)
[ -n "$pkgs" ] || { echo "no packages were built" >&2; exit 1; }

keyargs=""
if [ -n "${PKGOPS_KEY_FILE:-}" ]; then
  keyargs="--key $PKGOPS_KEY_FILE"
else
  echo "warning: PKGOPS_KEY_FILE not set, the repository will be UNSIGNED" >&2
fi

case "$MODE" in
none)
  echo "built:"; echo "$pkgs"
  ;;
pages)
  rm -rf "$SITE"
  if git ls-remote --exit-code --heads "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
    git clone --quiet --depth 1 --branch "$BRANCH" "$REMOTE" "$SITE"
  else
    git init --quiet "$SITE"
    git -C "$SITE" checkout --quiet --orphan "$BRANCH"
    git -C "$SITE" remote add origin "$REMOTE"
  fi
  repl=""
  [ "${PKGOPS_REPLACE:-0}" = 1 ] && repl="--replace"
  # shellcheck disable=SC2086
  "$PKGOPS" repo --repo "$SITE" $keyargs $repl --keep "$KEEP" "$DIST"
  touch "$SITE/.nojekyll"
  if [ -n "${PKGOPS_KEY_FILE:-}" ]; then
    "$PKGOPS" key public --key "$PKGOPS_KEY_FILE" > "$SITE/pkgops.pub"
  fi
  git -C "$SITE" config user.name "${GIT_AUTHOR_NAME:-github-actions[bot]}"
  git -C "$SITE" config user.email "${GIT_AUTHOR_EMAIL:-41898282+github-actions[bot]@users.noreply.github.com}"
  git -C "$SITE" add -A
  if git -C "$SITE" diff --cached --quiet; then
    echo "repository unchanged, nothing to publish"
    exit 0
  fi
  git -C "$SITE" commit --quiet -m "pkgops: update repository ($(basename "$(echo "$pkgs" | head -n1)"))"
  n=0
  until git -C "$SITE" push --quiet origin "HEAD:$BRANCH"; do
    n=$((n + 1))
    [ "$n" -lt 4 ] || { echo "push failed" >&2; exit 1; }
    git -C "$SITE" pull --quiet --rebase origin "$BRANCH"
  done
  echo "published to branch $BRANCH"
  ;;
release)
  version=$(sed -n 's/^version:[[:space:]]*//p' "$SPEC" | head -n1 | tr -d "\"'")
  TAG=${PKGOPS_TAG:-v$version}
  ASSETS=$WORK/assets
  mkdir -p "$ASSETS"
  for f in $pkgs; do
    abi=$(basename "$(dirname "$f")" | tr ':' '_')
    base=$(basename "$f" .pkg)
    cp "$f" "$ASSETS/$base-$abi.pkg"
  done
  # shellcheck disable=SC2086
  gh release view "$TAG" >/dev/null 2>&1 || gh release create "$TAG" --title "$TAG" --notes "FreeBSD packages built with pkgops"
  gh release upload "$TAG" "$ASSETS"/*.pkg --clobber
  echo "uploaded to release $TAG (install with: pkg add <asset URL>)"
  ;;
*)
  echo "unknown PKGOPS_PUBLISH=$MODE" >&2; exit 2 ;;
esac
