#!/bin/sh
# Clone the job's repository into the workspace and switch to the job's branch.
# A job that is run again after a lost runner goes on from what it pushed.
set -e
delay="${MACHINIST_CLONE_RETRY_DELAY:-2}"
attempt=1
while true; do
  status=0
  gh repo clone "$MACHINIST_REPO" . -- --quiet || status=$?
  if [ "$status" -eq 0 ]; then
    break
  fi
  if [ "$attempt" -ge 3 ]; then
    exit "$status"
  fi
  echo "checkout: clone failed (attempt $attempt of 3); retrying in ${delay}s" >&2
  sleep "$delay"
  find . -maxdepth 1 ! -name . -exec rm -rf -- {} +
  attempt=$((attempt + 1))
  delay=$((delay * 2))
done
git switch "$MACHINIST_BRANCH" 2> /dev/null || git switch -c "$MACHINIST_BRANCH"
