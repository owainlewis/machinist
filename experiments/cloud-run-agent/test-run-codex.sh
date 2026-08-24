#!/usr/bin/env bash

set -Eeuo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

readonly fake_bin="${temp_root}/bin"
readonly key_path="${temp_root}/api-key"
readonly args_path="${temp_root}/args"
readonly expected_args_path="${temp_root}/expected-args"
readonly observed_key_path="${temp_root}/observed-key"
mkdir -p "$fake_bin"

cat > "${fake_bin}/codex" <<'FAKE_CODEX'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\0' "$@" > "$FAKE_CODEX_ARGS_PATH"
printf '%s' "$CODEX_API_KEY" > "$FAKE_CODEX_KEY_PATH"
printf '{"type":"item.completed","item":{"type":"agent_message","text":"smoke test"}}\n'
FAKE_CODEX
chmod 0755 "${fake_bin}/codex"

printf 'test-key-with-newline\n' > "$key_path"

output="$({
    PATH="${fake_bin}:${PATH}" \
    PROMPT='- Reply with exactly: hello from Codex on Cloud Run' \
    CODEX_API_KEY_FILE="$key_path" \
    FAKE_CODEX_ARGS_PATH="$args_path" \
    FAKE_CODEX_KEY_PATH="$observed_key_path" \
        "${script_dir}/run-codex.sh"
} 2>&1)"
readonly output

printf '%s\0' \
    exec \
    --ephemeral \
    --skip-git-repo-check \
    --sandbox \
    read-only \
    --json \
    -- \
    '- Reply with exactly: hello from Codex on Cloud Run' \
    > "$expected_args_path"

if ! cmp -s "$expected_args_path" "$args_path"; then
    printf 'runner passed unexpected arguments to Codex\n' >&2
    exit 1
fi
if [[ "$(cat "$observed_key_path")" != test-key-with-newline ]]; then
    printf 'runner did not pass the normalized API key to Codex\n' >&2
    exit 1
fi
if [[ "$output" != *'"text":"smoke test"'* ]]; then
    printf 'runner did not forward Codex output\n' >&2
    exit 1
fi
if [[ "$output" == *test-key-with-newline* ]]; then
    printf 'runner leaked the API key in output\n' >&2
    exit 1
fi

if PATH="${fake_bin}:${PATH}" \
    CODEX_API_KEY_FILE="$key_path" \
    FAKE_CODEX_ARGS_PATH="$args_path" \
    FAKE_CODEX_KEY_PATH="$observed_key_path" \
        "${script_dir}/run-codex.sh" >/dev/null 2>&1; then
    printf 'runner accepted a missing prompt\n' >&2
    exit 1
fi

if PATH="${fake_bin}:${PATH}" \
    PROMPT=test \
    CODEX_API_KEY_FILE="${temp_root}/missing-key" \
    FAKE_CODEX_ARGS_PATH="$args_path" \
    FAKE_CODEX_KEY_PATH="$observed_key_path" \
        "${script_dir}/run-codex.sh" >/dev/null 2>&1; then
    printf 'runner accepted a missing API key file\n' >&2
    exit 1
fi

readonly empty_key_path="${temp_root}/empty-key"
: > "$empty_key_path"
if PATH="${fake_bin}:${PATH}" \
    PROMPT=test \
    CODEX_API_KEY_FILE="$empty_key_path" \
    FAKE_CODEX_ARGS_PATH="$args_path" \
    FAKE_CODEX_KEY_PATH="$observed_key_path" \
        "${script_dir}/run-codex.sh" >/dev/null 2>&1; then
    printf 'runner accepted an empty API key file\n' >&2
    exit 1
fi

printf 'run-codex tests passed\n'
