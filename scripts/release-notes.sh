#!/usr/bin/env bash
#
# Add the notes for a new version to the top of CHANGELOG.md.
#
#   scripts/release-notes.sh <new tag> [changelog file]
#
# Run it on the release-prep branch before tagging, so the pull request carries
# the new section. The notes cover every pull request merged since the last tag.

set -euo pipefail

version=${1:-}
changelog=${2:-CHANGELOG.md}
script_dir=$(cd "$(dirname "$0")" && pwd)

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH [changelog file]" >&2
  exit 2
fi

if [[ -f "$changelog" ]] && grep -qF "## $version (" "$changelog"; then
  echo "$changelog already has a section for $version" >&2
  exit 1
fi

previous=$("$script_dir/previous-tag.sh" "$version" HEAD)
notes=$("$script_dir/changelog.sh" "$previous" "$version" HEAD)

updated=$(mktemp)
trap 'rm -f "$updated"' EXIT

if [[ ! -f "$changelog" ]]; then
  printf '# Changelog\n\nEvery release of Machinist, newest first. `just release-notes vX.Y.Z` adds each new section.\n' >"$changelog"
fi

# Put the new section after the intro, above the newest release.
NOTES=$notes awk '
  !done && /^## / { printf "%s\n\n", ENVIRON["NOTES"]; done = 1 }
  { print }
  END { if (!done) printf "\n%s\n", ENVIRON["NOTES"] }
' "$changelog" >"$updated"

mv "$updated" "$changelog"
trap - EXIT
echo "Added $version to $changelog"
