#!/bin/sh
# Fix what the review found, then review the change again. The agent that made
# the fixes does not judge them: the fresh review's verdict is the one that
# counts. When the review asked for nothing, no agent runs.
set -e
[ "$(machinist verdict)" = revision_needed ] || exit 0
machinist agent revise
mv "$MACHINIST_DIR/review.json" "$MACHINIST_DIR/first-review.json"
machinist agent review
machinist verdict
