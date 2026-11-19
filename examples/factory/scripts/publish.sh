#!/bin/sh
# Push the branch and open one draft pull request, or say no change was needed.
# The body is assumptions.md, when the agent wrote one.
set -e
if test -z "$(git log --oneline "origin/HEAD..HEAD")"; then
  echo "The work needed no change." > "$MACHINIST_DIR/result.md"
  exit 0
fi
git push --quiet -u origin HEAD
if ! gh pr view "$MACHINIST_BRANCH" > /dev/null 2>&1; then
  : > "$MACHINIST_DIR/body.md"
  if test -s "$MACHINIST_DIR/assumptions.md"; then
    { echo "## Assumptions"; echo; cat "$MACHINIST_DIR/assumptions.md"; } > "$MACHINIST_DIR/body.md"
  fi
  title=$(git log --reverse --format=%s "origin/HEAD..HEAD" | head -n 1)
  gh pr create --draft --head "$MACHINIST_BRANCH" --title "$title" --body-file "$MACHINIST_DIR/body.md" > /dev/null
fi
gh pr view "$MACHINIST_BRANCH" --json url --jq .url > "$MACHINIST_DIR/result.md"
