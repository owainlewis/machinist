#!/usr/bin/env bash

set -euo pipefail

if [[ $(id -u) -ne 0 ]]; then
  echo "run this script as root" >&2
  exit 1
fi

if [[ ! -r /etc/os-release ]]; then
  echo "unsupported Linux distribution: /etc/os-release is missing" >&2
  exit 1
fi
. /etc/os-release
if [[ ${ID:-} != ubuntu && ${ID:-} != debian ]]; then
  echo "unsupported Linux distribution: ${ID:-unknown}" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl git gh jq openssh-client tar

# Codex and Claude Code use per-user credentials, so install their standalone
# distributions as the account that will run Machinist.
curl -fsSL https://chatgpt.com/codex/install.sh | sh
curl -fsSL https://claude.ai/install.sh | bash
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/main/install.sh | sh

machinist init

cat <<'EOF'

VM bootstrap complete.

Next steps:
  1. Run `gh auth login`.
  2. Run `codex` once and sign in.
  3. Run `claude` once and sign in.
  4. Clone each repository agents may use and register its absolute path in
     ~/.machinist/worker.toml.
  5. Start `machinist start` and `machinist worker start`.

Keep the control plane on 127.0.0.1. Reach it from your computer with:
  ssh -N -L 7331:127.0.0.1:7331 machinist
EOF
