#!/usr/bin/env bash
#
# Print the release tag before a new version, or nothing for the first release.
#
#   scripts/previous-tag.sh <new tag> [end ref]
#
# It looks back from the end ref, which defaults to the new tag. When the new
# tag already points at the end ref, it starts from the parent instead, so the
# new tag is not its own previous one.
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

start=$end_ref
tagged=$(git rev-parse --verify --quiet "refs/tags/$version^{commit}" || true)
if [[ -n "$tagged" && "$tagged" == "$(git rev-parse --verify "$end_ref^{commit}")" ]]; then
  if ! git rev-parse --verify --quiet "$end_ref^" >/dev/null; then
    exit 0
  fi
  start="$end_ref^"
fi
git describe "${flags[@]}" "$start" 2>/dev/null || true
