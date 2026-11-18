#!/bin/sh
# Clone the job's repository into the workspace and switch to the job's branch.
# A job that answered a question goes on from the branch it pushed before.
set -e
gh repo clone "$MACHINIST_REPO" . -- --quiet
git switch "$MACHINIST_BRANCH" 2> /dev/null || git switch -c "$MACHINIST_BRANCH"
