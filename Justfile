set shell := ["bash", "-euo", "pipefail", "-c"]

frontend:
    cd internal/controlplane/web && npm ci && npm run build

build: frontend
    mkdir -p bin
    go build -o bin/machinist ./cmd/machinist

# Start the local control plane.
control-plane: build
    ./bin/machinist start

# Start the local managed worker.
worker: build
    ./bin/machinist worker start

# Start the local control plane and managed worker together.
local: build
    #!/usr/bin/env bash
    set -euo pipefail

    cleanup() {
        trap - EXIT INT TERM
        while IFS= read -r child_pid; do
            kill "$child_pid" 2>/dev/null || true
        done < <(jobs -pr)
        wait 2>/dev/null || true
    }
    trap cleanup EXIT
    trap 'exit 130' INT TERM

    ./bin/machinist start &
    control_plane_pid=$!
    ./bin/machinist worker start &
    worker_pid=$!

    status=0
    while true; do
        if ! kill -0 "$control_plane_pid" 2>/dev/null; then
            wait "$control_plane_pid" || status=$?
            break
        fi
        if ! kill -0 "$worker_pid" 2>/dev/null; then
            wait "$worker_pid" || status=$?
            break
        fi
        sleep 1
    done
    exit "$status"

test:
    go test -race ./...

format-check:
    files="$(gofmt -l cmd internal)"; test -z "$files" || { printf '%s\n' "$files"; exit 1; }

check:
    cd internal/controlplane/web && npm ci && npm test && npm run build
    just format-check
    go vet ./...
    go test -race ./...
    go build ./...
