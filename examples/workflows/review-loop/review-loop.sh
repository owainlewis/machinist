#!/bin/sh
set -eu

prompt=$(cat)
max_repairs=3
repair=0

printf '%s\n' "implement"
printf '%s\n' "$prompt" | codex exec --json -

while :; do
  printf '%s\n' "wait for review: attempt $((repair + 1))/$((max_repairs + 1))"
  if ./scripts/wait-for-review.sh; then
    printf '%s\n' "review approved"
    exit 0
  fi
  if [ "$repair" -eq "$max_repairs" ]; then
    break
  fi
  repair=$((repair + 1))
  printf '%s\n' "fix feedback: repair $repair/$max_repairs"
  feedback=$(./scripts/read-review-feedback.sh)
  printf '%s\n' "$feedback" | codex exec --json -
done

printf '%s\n' "review was not approved after $max_repairs repairs" >&2
exit 1
