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

# --- release (always straight on master, no PR) ---

# Cut a release, e.g. `just release v0.1.0`: checks, version.txt, CHANGELOG.md, commit, tag, push master + tag.
# The tag push runs .github/workflows/release.yml. Run by a person only; see docs/releasing.md.
release VERSION:
    bash scripts/release.sh {{VERSION}}

# Same checks as `release`, changes and pushes nothing.
release-dry VERSION:
    DRY=1 bash scripts/release.sh {{VERSION}}

# A local trial of the release archives in dist/, publishing nothing.
snapshot:
    goreleaser release --snapshot --clean --skip=publish
