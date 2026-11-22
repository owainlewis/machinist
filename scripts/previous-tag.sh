#!/usr/bin/env bash
#
# Print the release tag before a new version, or nothing for the first release.
#
#   scripts/previous-tag.sh <new tag> [end ref]
#
# It looks back from the parent of the end ref, which defaults to the new tag.
# A stable version skips prereleases, so v0.5.0 follows v0.4.0, not v0.5.0-rc.1.

set -euo pipefail

version=${1:-}
end_ref=${2:-$version}

if [[ -z "$version" ]]; then
  echo "usage: $0 <new tag> [end ref]" >&2
  exit 2
fi

flags=(--tags --abbrev=0 --match 'v[0-9]*')
if [[ "$version" != *-* ]]; then
  flags+=(--exclude '*-*')
fi

if ! git rev-parse --verify --quiet "$end_ref^" >/dev/null; then
  exit 0
fi
git describe "${flags[@]}" "$end_ref^" 2>/dev/null || true
