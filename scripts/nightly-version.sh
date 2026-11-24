#!/usr/bin/env bash
#
# Print the next nightly prerelease version for the current commit, based on
# the latest stable vMAJOR.MINOR.PATCH tag reachable from HEAD:
#
#   scripts/nightly-version.sh [YYYYMMDD] [short-sha]
#
# Example: if the latest stable tag is v0.5.0, this prints:
#   v0.5.1-nightly.20261124.ab12cd3
#
# When no stable tag is reachable from HEAD, it starts from v0.0.1-nightly....

set -euo pipefail

date_stamp=${1:-$(date -u +%Y%m%d)}
short_sha=${2:-$(git rev-parse --short=7 HEAD)}

if [[ ! "$date_stamp" =~ ^[0-9]{8}$ ]]; then
  echo "invalid date stamp \"$date_stamp\"; expected YYYYMMDD" >&2
  exit 2
fi
if [[ ! "$short_sha" =~ ^[0-9a-f]{7,40}$ ]]; then
  echo "invalid short commit SHA \"$short_sha\"; expected lowercase hex" >&2
  exit 2
fi

latest_stable=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' HEAD 2>/dev/null || true)
if [[ -z "$latest_stable" ]]; then
  major=0
  minor=0
  patch=0
elif [[ "$latest_stable" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  major=${BASH_REMATCH[1]}
  minor=${BASH_REMATCH[2]}
  patch=${BASH_REMATCH[3]}
else
  echo "unexpected stable tag \"$latest_stable\"; expected vMAJOR.MINOR.PATCH" >&2
  exit 1
fi

next_patch=$((patch + 1))
printf 'v%d.%d.%d-nightly.%s.%s\n' "$major" "$minor" "$next_patch" "$date_stamp" "$short_sha"
