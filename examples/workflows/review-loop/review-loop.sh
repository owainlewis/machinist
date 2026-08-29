#!/bin/sh
set -eu

prompt=$(cat)
max_repairs=3
repair=0

printf '%s\n' "implement"
printf '%s\n' "$prompt" | codex exec --json -

while [ "$repair" -lt "$max_repairs" ]; do
  printf '%s\n' "wait for review: attempt $((repair + 1))/$max_repairs"
  if ./scripts/wait-for-review.sh; then
    printf '%s\n' "review approved"
    exit 0
  fi
  repair=$((repair + 1))
  printf '%s\n' "fix feedback: repair $repair/$max_repairs"
  ./scripts/read-review-feedback.sh | codex exec --json -
done

printf '%s\n' "review was not approved after $max_repairs repairs" >&2
exit 1
