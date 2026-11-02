#!/bin/sh
# check-actions-pinned.sh [FILE|DIR ...]
#
# Every third-party action a workflow or composite action uses must be pinned to a full 40-hex commit SHA:
#   uses: owner/repo@0123456789abcdef0123456789abcdef01234567   # v1.2.3
# Allowed without a SHA: first-party actions (ACTIONS_FIRST_PARTY, default "actions/ github/"), local actions
# (./path) and docker:// images. Default arguments: .github and action.yml when they exist.
#
# ACTIONS_PIN_ENFORCE=1 fails on an unpinned action; anything else only warns (exit 0). docs/supply-chain.md has the
# commands that find the SHA of each tag.
set -eu
FIRST_PARTY=${ACTIONS_FIRST_PARTY:-"actions/ github/"}
ENFORCE=${ACTIONS_PIN_ENFORCE:-0}
if [ $# -eq 0 ]; then
  for d in .github action.yml action.yaml; do [ -e "$d" ] && set -- "$@" "$d"; done
fi
[ $# -gt 0 ] || { echo "check-actions-pinned: nothing to check"; exit 0; }

files=$(find "$@" -type f \( -name '*.yml' -o -name '*.yaml' \) | sort)
out=$(mktemp)
trap 'rm -f "$out"' EXIT
for f in $files; do
  # "- uses: x" and "uses: x", without quotes and trailing comments
  grep -n -E '^[[:space:]]*(-[[:space:]]+)?uses:' "$f" | while IFS= read -r line; do
    n=${line%%:*}
    v=$(echo "${line#*:}" | sed -e 's/^[[:space:]]*-\{0,1\}[[:space:]]*uses:[[:space:]]*//' -e 's/[[:space:]]#.*$//' \
      -e 's/[[:space:]]*$//' -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/")
    case "$v" in
      ./*|docker://*|'') continue ;;
    esac
    first=0
    for p in $FIRST_PARTY; do
      case "$v" in "$p"*) first=1 ;; esac
    done
    [ "$first" = 1 ] && continue
    ref=${v##*@}
    if [ "$ref" = "$v" ] || ! echo "$ref" | grep -Eq '^[0-9a-f]{40}$'; then
      repo=$(echo "${v%@*}" | cut -d/ -f1-2)
      msg="$f:$n: $v is not pinned to a commit SHA (gh api repos/$repo/commits/$ref --jq .sha)"
      if [ "$ENFORCE" = 1 ]; then
        if [ "${GITHUB_ACTIONS:-}" = true ]; then echo "::error file=$f,line=$n::$msg"; else echo "error: $msg"; fi
      else
        if [ "${GITHUB_ACTIONS:-}" = true ]; then echo "::warning file=$f,line=$n::$msg"; else echo "warning: $msg"; fi
      fi
      echo x
    fi
  done
done > "$out"
bad=$(grep -c '^x$' "$out" || true)
grep -v '^x$' "$out" || true
if [ "$bad" -gt 0 ]; then
  if [ "$ENFORCE" = 1 ]; then
    echo "check-actions-pinned: $bad unpinned third-party action(s)"
    exit 1
  fi
  echo "check-actions-pinned: $bad unpinned third-party action(s); warning only until ACTIONS_PIN_ENFORCE=1"
  exit 0
fi
echo "check-actions-pinned: every third-party action is pinned to a commit SHA"
