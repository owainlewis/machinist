#!/bin/sh
# Run the repository's tests with the usual command for its language.
set -e
if test -f package.json; then
  test -d node_modules || npm ci --no-audit --no-fund
  npm test
elif test -f go.mod; then go test ./...
elif test -f Cargo.toml; then cargo test
elif test -f pyproject.toml; then python -m pytest
else echo "no test command for this repository" >&2; exit 1
fi
