#!/bin/sh
# Clone the job's repository into the workspace and switch to the job's branch.
# A job that is run again after a lost runner goes on from what it pushed.
set -e
gh repo clone "$MACHINIST_REPO" . -- --quiet
git switch "$MACHINIST_BRANCH" 2> /dev/null || git switch -c "$MACHINIST_BRANCH"
