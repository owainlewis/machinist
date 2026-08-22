set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Build operator binaries from committed embedded UI assets. Node is not used.
build:
    #!/usr/bin/env bash
    set -euo pipefail
    data_home="${FACTORY_DATA_HOME:-${FACTORY_V2_DATA_HOME:-${HOME:?HOME or FACTORY_DATA_HOME is required}/.factory}}"
    build_directory="${FACTORY_BUILD_DIR:-${FACTORY_V2_BUILD_DIR:-$data_home/bin}}"
    mkdir -p "$build_directory"
    go build -o "$build_directory/factory" ./cmd/factory
    go build -o "$build_directory/factory-server" ./cmd/factory-server
    go build -o "$build_directory/factory-worker" ./cmd/factory-worker
    printf 'Factory binaries built in %s\n' "$build_directory"

# Start one control plane and worker. Pass a worker config path when needed.
run config="":
    @if [[ -n "{{config}}" ]]; then ./scripts/run-local.sh "{{config}}"; else ./scripts/run-local.sh; fi

# Install pinned UI dependencies.
ui-install:
    cd web && npm ci

# Rebuild committed embedded UI assets. Pass 0 to reuse installed dependencies.
ui-build install="1":
    @if [[ "{{install}}" == "1" ]]; then cd web && npm ci; fi
    cd web && npm run build

# Run UI lint, type checks, and component tests.
ui-check:
    cd web && npm run lint
    cd web && npm run typecheck
    cd web && npm test

# Run browser tests against the real Go server.
test-browser:
    cd web && npm run test:browser

# Report Go files that need formatting.
format-check:
    @test -z "$(find cmd internal migrations web -path web/node_modules -prune -o -name '*.go' -exec gofmt -l {} +)"

# Run Go static analysis.
vet:
    go vet ./...

# Fail on reachable Go vulnerabilities using the supported patched toolchain.
vuln:
    go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

# Run correctness and dead-code checks without style-only churn.
staticcheck:
    go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -checks 'SA*,U1000' ./...

# Prove workers do not import control-plane implementation code.
boundary:
    @! go list -deps ./internal/worker | grep -qx 'github.com/owainlewis/factory/internal/controlplane'

# Run all Go tests.
test:
    go test -timeout 5m ./...

# Race-check the worker package, including coordination and process cancellation.
test-worker-race:
    #!/usr/bin/env bash
    set -euo pipefail
    log="$(mktemp)"
    trap 'rm -f "$log"' EXIT
    go test -timeout 5m -race -count=1 -v ./internal/worker 2>&1 | tee "$log"
    count="$(grep -c '^=== RUN   Test' "$log" || true)"
    if [[ "$count" -eq 0 ]]; then
        printf 'test-worker-race selected zero tests in ./internal/worker\n' >&2
        exit 1
    fi
    printf 'test-worker-race ran %s tests with the race detector\n' "$count"

# Test the Node-free build and Just command surface.
test-tooling:
    ./scripts/test-build.sh
    ./scripts/test-update-go-minimum.sh

# Test local startup, readiness, and signal handling.
test-launcher:
    ./scripts/test-run-local.sh

# Build a tagged release set from the current checkout.
release version commit output="dist":
    ./scripts/release.sh "{{version}}" "{{commit}}" "{{output}}"

# Rebuild twice and verify every release target and native version output.
test-release:
    ./scripts/test-release.sh

# Run the normal local and CI checks, excluding the slower browser suite.
check: format-check vet vuln staticcheck boundary test ui-check test-tooling test-launcher
