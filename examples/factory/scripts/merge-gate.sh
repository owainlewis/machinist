#!/bin/sh
# Judge whether the pull request is safe to auto-merge under the merge policy.
# Runs without GitHub tokens so the judge is strictly local and read-only.
# Skips branches with no commits beyond the default branch or unaccepted reviews.
set -e
rm -f "$MACHINIST_DIR/merge-gate.json"
ahead=$(git rev-list --count origin/HEAD..HEAD)
if [ "$ahead" = 0 ]; then
  exit 0
fi
if [ -f "$MACHINIST_DIR/review.json" ] && [ "$(machinist verdict)" != accepted ]; then
  exit 0
fi
env -u GH_TOKEN -u GITHUB_TOKEN machinist agent merge-gate
machinist gate
