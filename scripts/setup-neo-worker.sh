#!/usr/bin/env bash
# Run as the worker account. Install the linter version pinned by Neo's CI.
set -euo pipefail
neo_repo=${1:?Usage: bash scripts/setup-neo-worker.sh /absolute/path/to/neo}
workflow="$neo_repo/.github/workflows/ci.yml"
linter_version=$(sed -n '/uses: golangci\/golangci-lint-action@/,/version:/s/^[[:space:]]*version: \(v[0-9][0-9.]*\)[[:space:]]*$/\1/p' "$workflow")
if [[ ! $linter_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Cannot determine the pinned golangci-lint version from $workflow" >&2
  exit 1
fi
for tool in go git gh; do
  command -v "$tool" >/dev/null || { echo "Missing worker prerequisite: $tool" >&2; exit 1; }
done
worker_bin=${GOBIN:-$(go env GOPATH)/bin}
GOBIN="$worker_bin" go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$linter_version"
"$worker_bin/golangci-lint" version
echo "Installed $worker_bin/golangci-lint; ensure this directory is on the worker service PATH."
