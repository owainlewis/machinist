#!/usr/bin/env bash

set -euo pipefail

if [[ $(id -u) -ne 0 ]]; then
  echo "run this script as root" >&2
  exit 1
fi

machinist_version=${MACHINIST_VERSION:-}
if [[ ! $machinist_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "set MACHINIST_VERSION to the release being installed, such as v0.2.0" >&2
  exit 2
fi

legacy_root_install=false
if [[ -d /root/.machinist ]]; then
  legacy_root_install=true
fi
for legacy_unit in machinist-control-plane.service machinist-worker.service; do
  if [[ -f /etc/systemd/system/$legacy_unit ]] && grep -q '^User=root$' "/etc/systemd/system/$legacy_unit"; then
    legacy_root_install=true
  fi
done
if [[ $legacy_root_install == true ]]; then
  echo "legacy root-based Machinist installation detected" >&2
  echo "follow the v0.1.x migration steps in docs/vm-deployment.md before running this bootstrap" >&2
  exit 3
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

runtime_user=machinist
if ! id "$runtime_user" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$runtime_user"
fi
runtime_home=$(getent passwd "$runtime_user" | cut -d: -f6)
if [[ -z $runtime_home || ! -d $runtime_home ]]; then
  echo "could not determine home directory for $runtime_user" >&2
  exit 1
fi

# Codex and Claude Code use per-user credentials, so install their standalone
# distributions as the account that will run Machinist.
runuser -u "$runtime_user" -- env HOME="$runtime_home" \
  bash -c 'cd "$HOME"; curl -fsSL https://chatgpt.com/codex/install.sh | sh'
runuser -u "$runtime_user" -- env HOME="$runtime_home" \
  bash -c 'cd "$HOME"; curl -fsSL https://claude.ai/install.sh | bash'
curl -fsSL "https://raw.githubusercontent.com/owainlewis/machinist/$machinist_version/install.sh" | \
  env MACHINIST_VERSION="$machinist_version" sh

# Standalone agent installers use ~/.local/bin. Login shells commonly add that
# directory to PATH, but services and other non-interactive processes do not.
for agent_command in codex claude; do
  agent_path="$runtime_home/.local/bin/$agent_command"
  if [[ ! -x "$agent_path" ]]; then
    echo "$agent_command installer did not create $agent_path" >&2
    exit 1
  fi
  ln -sfn "$agent_path" "/usr/local/bin/$agent_command"
done

runuser -u "$runtime_user" -- env HOME="$runtime_home" \
  bash -c 'cd "$HOME"; exec machinist init'

worker_was_enabled=false
worker_was_active=false
if systemctl is-enabled --quiet machinist-worker.service 2>/dev/null; then
  worker_was_enabled=true
fi
if systemctl is-active --quiet machinist-worker.service 2>/dev/null; then
  worker_was_active=true
fi

service_base_url="https://raw.githubusercontent.com/owainlewis/machinist/$machinist_version/deploy/systemd"
service_tmp_dir=$(mktemp -d)
trap 'rm -rf "$service_tmp_dir"' EXIT
curl -fsSL "$service_base_url/machinist-control-plane.service" \
  -o "$service_tmp_dir/machinist-control-plane.service"
curl -fsSL "$service_base_url/machinist-worker.service" \
  -o "$service_tmp_dir/machinist-worker.service"
install -m 0644 "$service_tmp_dir/machinist-control-plane.service" \
  /etc/systemd/system/machinist-control-plane.service
install -m 0644 "$service_tmp_dir/machinist-worker.service" \
  /etc/systemd/system/machinist-worker.service
systemctl daemon-reload
systemctl enable machinist-control-plane.service
systemctl restart machinist-control-plane.service
if runuser -u "$runtime_user" -- env HOME="$runtime_home" \
  bash -c 'cd "$HOME"; exec machinist worker validate --help' >/dev/null 2>&1; then
  if runuser -u "$runtime_user" -- env HOME="$runtime_home" \
    bash -c 'cd "$HOME"; exec machinist worker validate' >/dev/null 2>&1; then
    systemctl enable machinist-worker.service
    systemctl restart machinist-worker.service
  else
    systemctl disable --now machinist-worker.service
  fi
else
  if [[ $worker_was_active == true ]]; then
    if [[ $worker_was_enabled == true ]]; then
      systemctl enable machinist-worker.service
    else
      systemctl disable machinist-worker.service
    fi
    systemctl restart machinist-worker.service
    echo "installed Machinist release does not support worker validation; restored the previously active worker" >&2
  else
    if [[ $worker_was_enabled == true ]]; then
      systemctl enable machinist-worker.service
      systemctl stop machinist-worker.service
      echo "installed Machinist release does not support worker validation; preserved the enabled but inactive worker" >&2
    else
      systemctl disable --now machinist-worker.service
    fi
  fi
fi

cat <<'EOF'

VM bootstrap complete.

Next steps:
  1. Run `su - machinist`, then complete the remaining login and repository steps as that user.
  2. Run `gh auth login`.
  3. Run `codex` once and sign in.
  4. Run `claude` once and sign in.
  5. Clone each repository agents may use and register its absolute path in
     ~/.machinist/worker.toml.
  6. Exit back to root and run `systemctl enable --now machinist-worker` after registering a repository.
  7. Check `systemctl status machinist-control-plane machinist-worker`.

Keep the control plane on 127.0.0.1. Reach it from your computer with:
  ssh -N -L 7331:127.0.0.1:7331 machinist
EOF
