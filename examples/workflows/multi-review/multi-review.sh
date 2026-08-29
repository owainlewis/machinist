#!/bin/sh
set -eu

prompt=$(cat)
printf '%s\n' "stage 1/2: Codex review"
printf '%s\n' "$prompt" | codex exec --json -
printf '%s\n' "stage 2/2: Claude review"
printf '%s\n' "$prompt" | claude -p
