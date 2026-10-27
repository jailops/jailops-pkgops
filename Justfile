
# go.mod names the Go it needs: let the go command fetch that toolchain instead of failing on an older local Go
export GOTOOLCHAIN := "auto"
# pkgops development tasks (https://github.com/casey/just)

default: test

# build the CLI into ./bin, stamped with version.txt, the commit and the date
build:
    #!/usr/bin/env bash
    set -euo pipefail
    v=$(tr -d '[:space:]' < version.txt)
    c=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
    d=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$v -X main.commit=$c -X main.date=$d" -o bin/pkgops ./cmd/pkgops

# cross-compile the CLI itself for FreeBSD (so pkgops can run on a FreeBSD host)
build-freebsd:
    GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/pkgops-freebsd-amd64 ./cmd/pkgops
    GOOS=freebsd GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o bin/pkgops-freebsd-arm64 ./cmd/pkgops

test:
    go test ./...

# race detector, no -short
test-race:
    go test -race -count=1 ./...

# rewrite testdata/golden after an intentional format change; review the diff!
golden:
    go test -run 'Golden' -update .

vet:
    go vet ./...

fmt:
    gofmt -w .

fmt-check:
    test -z "$(gofmt -l .)"

# web UI: install, typecheck, unit tests, build into internal/webui/dist
web:
    cd web && npm ci && npm run typecheck && npm test && npm run build

# Playwright against a real seeded registry (needs Chromium: npx playwright install chromium)
e2e:
    cd web && npm run e2e

# everything CI runs
ci: fmt-check vet test-race

# build the example package for both ABIs into ./dist
example: build
    cd examples/hello && SOURCE_DATE_EPOCH=1700000000 ../../bin/pkgops build --abi FreeBSD:15:amd64 -o ../../dist
    cd examples/hello && SOURCE_DATE_EPOCH=1700000000 ../../bin/pkgops build --abi FreeBSD:15:aarch64 -o ../../dist

# ── Release ──────────────────────────────────────────────────────────────
#
# Two-step, PR-based release flow:
#   1. just release-pr 0.2.0   -> branch release/v0.2.0 with version.txt and the CHANGELOG.md section, and a PR (review, CI)
#   2. merge the PR
#   3. just release 0.2.0      -> verifies master carries 0.2.0, signs the tag, pushes it. The release workflow checks
#                                the tagged commit and GoReleaser publishes the GitHub release. No tests run here.
# The very first release has nothing to PR: version.txt and CHANGELOG.md already say VERSION, go straight to step 3.

# Step 1: open the release PR (version bump and changelog section).
release-pr VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    # Bare X.Y.Z only: the recipe adds the `v`. Rejects "v0.2.0" (-> vv0.2.0).
    [[ "{{VERSION}}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "x version must be X.Y.Z (no leading v), got '{{VERSION}}'"; exit 1; }
    [ -z "$(git status --porcelain)" ] || { echo "x working tree not clean"; exit 1; }
    git fetch origin
    if [ "$(git show origin/master:version.txt | tr -d '[:space:]')" = "{{VERSION}}" ]; then
        echo "x version.txt on master is already {{VERSION}}: nothing to bump; run: just release {{VERSION}}"
        exit 1
    fi
    git ls-remote --exit-code --heads origin "release/v{{VERSION}}" >/dev/null 2>&1 && \
        { echo "x release/v{{VERSION}} already on origin: open the PR with: gh pr create --head release/v{{VERSION}} --title \"Release v{{VERSION}}\" (or delete it: git push -d origin release/v{{VERSION}})"; exit 1; }
    git show-ref --verify -q "refs/heads/release/v{{VERSION}}" && \
        { echo "x local branch release/v{{VERSION}} left over from a failed run: git checkout master && git branch -D release/v{{VERSION}}"; exit 1; }
    trap 'git checkout master 2>/dev/null; git branch -D "release/v{{VERSION}}" 2>/dev/null || true' EXIT
    git checkout -b "release/v{{VERSION}}" origin/master
    echo "{{VERSION}}" > version.txt
    # CHANGELOG.md: the Unreleased entries move under the new version, a new empty Unreleased stays on top
    grep -Eq '^## \[?Unreleased\]?' CHANGELOG.md || { echo "x CHANGELOG.md has no Unreleased section"; exit 1; }
    body=$(awk '/^## \[?Unreleased\]?/{f=1;next} /^## /{f=0} f' CHANGELOG.md | tr -d '[:space:]')
    [ -n "$body" ] || { echo "x the Unreleased section of CHANGELOG.md is empty: write the notes first"; exit 1; }
    awk -v v="{{VERSION}}" -v d="$(date -u +%Y-%m-%d)" '
      /^## \[?Unreleased\]?/ && !done { print; print ""; if ($0 ~ /\[/) print "## [" v "] - " d; else print "## " v " - " d; done=1; next }
      { print }' CHANGELOG.md > CHANGELOG.md.new && mv CHANGELOG.md.new CHANGELOG.md
    git add version.txt CHANGELOG.md
    git commit -m "Release v{{VERSION}}"
    git push -u origin "release/v{{VERSION}}"
    trap - EXIT
    gh pr create --title "Release v{{VERSION}}" \
        --body "Bumps version.txt to {{VERSION}} and moves the Unreleased changelog entries under it. After merge: \`just release {{VERSION}}\` tags master and CI publishes the release." \
        || { echo "x branch pushed but PR creation failed: finish with: gh pr create --head release/v{{VERSION}} --title \"Release v{{VERSION}}\""; exit 1; }
    echo "release PR opened: merge it, then run: just release {{VERSION}}"

# Step 2 (after the PR is merged, or straight away for the first release): tag master and push the tag.
release VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    [[ "{{VERSION}}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "x version must be X.Y.Z (no leading v), got '{{VERSION}}'"; exit 1; }
    [ -z "$(git status --porcelain)" ] || { echo "x working tree not clean"; exit 1; }
    git checkout master
    git pull --ff-only
    git fetch --tags origin  # catch remote-only tags the local repo has not seen
    [ "$(tr -d '[:space:]' < version.txt)" = "{{VERSION}}" ] || \
        { echo "x version.txt is '$(cat version.txt)': merge the release PR first"; exit 1; }
    grep -Eq '^## \[?{{VERSION}}\]?( |$)' CHANGELOG.md || \
        { echo "x CHANGELOG.md has no section for {{VERSION}}"; exit 1; }
    git rev-parse "v{{VERSION}}" >/dev/null 2>&1 && { echo "x tag v{{VERSION}} already exists"; exit 1; }
    git tag -s "v{{VERSION}}" -m "v{{VERSION}}"
    git push origin "v{{VERSION}}"
    echo "v{{VERSION}} tagged: CI builds the release: gh run watch"

# A local trial of the release archives in dist/, publishing nothing.
snapshot:
    goreleaser release --snapshot --clean --skip=publish
