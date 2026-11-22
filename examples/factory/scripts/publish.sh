#!/bin/sh
# Push the branch, then have agents/publish.md open the pull request. A push
# GitHub rejects fails the step with GitHub's answer, so the job fails and the
# merge step never runs. A branch with no commits beyond the default branch
# needed no change, so nothing is pushed.
set -e
ahead=$(git rev-list --count origin/HEAD..HEAD)
if [ "$ahead" = 0 ]; then
  [ -s "$MACHINIST_DIR/result.md" ] || echo "The work needed no change." > "$MACHINIST_DIR/result.md"
  exit 0
fi
machinist push
machinist agent publish
