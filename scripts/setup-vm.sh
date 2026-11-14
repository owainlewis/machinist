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
  bash -c 'curl -fsSL https://chatgpt.com/codex/install.sh | sh'
runuser -u "$runtime_user" -- env HOME="$runtime_home" \
  bash -c 'curl -fsSL https://claude.ai/install.sh | bash'
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

runuser -u "$runtime_user" -- env HOME="$runtime_home" machinist init

service_base_url="https://raw.githubusercontent.com/owainlewis/machinist/$machinist_version/deploy/systemd"
service_tmp_dir=$(mktemp -d)
trap 'rm -rf "$service_tmp_dir"' EXIT
curl -fsSL "$service_base_url/machinist-control-plane.service" \
  -o "$service_tmp_dir/machinist-control-plane.service"
curl -fsSL "$service_base_url/machinist-runner.service" \
  -o "$service_tmp_dir/machinist-runner.service"
install -m 0644 "$service_tmp_dir/machinist-control-plane.service" \
  /etc/systemd/system/machinist-control-plane.service
install -m 0644 "$service_tmp_dir/machinist-runner.service" \
  /etc/systemd/system/machinist-runner.service
systemctl daemon-reload
systemctl enable machinist-control-plane.service
systemctl restart machinist-control-plane.service
# The runner needs credentials first, so the operator enables it in the steps below.
systemctl disable --now machinist-worker.service 2>/dev/null || true

cat <<'EOF'

VM bootstrap complete.

Next steps:
  1. Run `su - machinist`, then edit ~/.machinist/config.toml and set github.handle
     and github.repositories.
  2. Edit ~/.machinist/runner.toml and choose the credential for each harness.
  3. Exit back to root. Put GH_TOKEN, for the bot account, and those credentials in
     /etc/machinist/runner.env. Put GH_TOKEN in /etc/machinist/control-plane.env.
     Make both files readable only by root.
  4. Run `systemctl restart machinist-control-plane`.
  5. Run `su - machinist -c 'set -a; . /etc/machinist/runner.env; machinist runner check --repo owner/name'` as root.
  6. Run `systemctl enable --now machinist-runner`.
  7. Check `systemctl status machinist-control-plane machinist-runner`.

Keep the control plane on 127.0.0.1. Reach it from your computer with:
  ssh -N -L 7331:127.0.0.1:7331 machinist
EOF
