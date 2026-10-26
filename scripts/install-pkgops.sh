#!/bin/sh
# Put the pkgops binary of a release tag in DEST, from the GitHub release (sha256 verified against the
# release's checksums.txt), or else built from a source checkout. Used by action.yml and gh-publish.sh.
#
#   install-pkgops.sh [DEST_DIR]
#
# Environment:
#   PKGOPS_VERSION  release tag to download, e.g. v0.1.0. Empty, or not a vX.Y.Z tag (a branch, a commit SHA):
#                   no download, build from source.
#   PKGOPS_REPO     GitHub owner/name of the releases        (default: jailops/pkgops)
#   PKGOPS_BASE_URL download base, for tests        (default: https://github.com/$PKGOPS_REPO/releases/download)
#   PKGOPS_SRC      source checkout for the fallback build   (default: the repository this script is in)
#
# Prints the final directory holding `pkgops` on stdout's last line. Works on linux and darwin, amd64 and arm64.
# Note: `go install ./cmd/pkgops@TAG` is not valid Go, and `go install github.com/jailops/pkgops/...@TAG` fails
# because the module is jailops.net/pkgops; the source fallback therefore builds the checkout that is at the tag.
set -eu

DEST=${1:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/pkgops-bin}
TAG=${PKGOPS_VERSION:-}
REPO=${PKGOPS_REPO:-jailops/pkgops}
SRC=${PKGOPS_SRC:-$(cd "$(dirname "$0")/.." && pwd)}
mkdir -p "$DEST"

build_from_source() {
  echo "pkgops: building from source in $SRC" >&2
  (cd "$SRC" && CGO_ENABLED=0 go build -trimpath -o "$DEST/pkgops" ./cmd/pkgops)
}

download() {
  case "$TAG" in v[0-9]*.[0-9]*.[0-9]*) ;; *) return 1 ;; esac
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in linux | darwin) ;; *) return 1 ;; esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) return 1 ;;
  esac
  ver=${TAG#v}
  file="pkgops_${ver}_${os}_${arch}.tar.gz"
  base="${PKGOPS_BASE_URL:-https://github.com/$REPO/releases/download}/$TAG"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/$file" "$base/$file" || return 1
  curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/checksums.txt" "$base/checksums.txt" || return 1
  want=$(awk -v f="$file" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$want" ] || { echo "pkgops: $file is not in checksums.txt" >&2; return 1; }
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$file" | awk '{ print $1 }')
  else
    got=$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')
  fi
  [ "$got" = "$want" ] || { echo "pkgops: sha256 mismatch for $file (want $want, got $got)" >&2; return 1; }
  tar -xzf "$tmp/$file" -C "$tmp" pkgops || return 1
  install -m 0755 "$tmp/pkgops" "$DEST/pkgops"
  echo "pkgops: downloaded and verified $file from the $TAG release" >&2
}

if [ -n "$TAG" ] && download; then
  :
else
  [ -z "$TAG" ] || echo "pkgops: release download failed for '$TAG', falling back to a source build" >&2
  build_from_source
fi
"$DEST/pkgops" version >&2
echo "$DEST"
