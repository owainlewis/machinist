#!/bin/sh
# Judge whether the pull request is safe to auto-merge under the merge policy.
# A merge-gate.json left by an earlier step is removed first, and branches with
# no commits beyond the default branch are skipped.
set -e
rm -f "$MACHINIST_DIR/merge-gate.json"
ahead=$(git rev-list --count origin/HEAD..HEAD)
if [ "$ahead" = 0 ]; then
  exit 0
fi
machinist agent merge-gate
machinist gate
