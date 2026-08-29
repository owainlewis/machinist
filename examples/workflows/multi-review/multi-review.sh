#!/bin/sh
set -eu

prompt=$(cat)
confirmation=$(printf '%s\n' "$prompt" | sed -n '1p')
if [ "$confirmation" != "trusted-head: yes" ]; then
  printf '%s\n' "refusing to review: first prompt line must be exactly 'trusted-head: yes'" >&2
  exit 2
fi

printf '%s\n' "stage 1/2: Codex review"
printf '%s\n' "$prompt" | codex exec --json -
printf '%s\n' "stage 2/2: Claude review"
printf '%s\n' "$prompt" | claude -p
