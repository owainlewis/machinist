#!/usr/bin/env bash

set -Eeuo pipefail

readonly prompt="${PROMPT:?PROMPT is required}"
readonly key_path="${CODEX_API_KEY_FILE:-/secrets/openai/api-key}"

if [[ ! -r "$key_path" ]]; then
    printf 'Codex API key file is unavailable: %s\n' "$key_path" >&2
    exit 1
fi

readonly api_key="$(tr -d '\r\n' < "$key_path")"
if [[ -z "$api_key" ]]; then
    printf 'Codex API key file is empty: %s\n' "$key_path" >&2
    exit 1
fi

CODEX_API_KEY="$api_key" \
    codex exec \
        --ephemeral \
        --skip-git-repo-check \
        --sandbox read-only \
        --json \
        -- \
        "$prompt"
