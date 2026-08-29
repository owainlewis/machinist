# Set up Machinist on a remote VM

This guide sets up a dedicated Ubuntu or Debian VM that runs Machinist with
Codex and Claude Code. Machinist runs from a published binary. Git checkouts are
needed only for repositories that coding agents may inspect or change.

The examples use a local SSH alias named `machinist`. Root performs bootstrap
and service administration, while coding agents run as a dedicated unprivileged
`machinist` account.

## Understand the four logins

The VM needs separate credentials for separate jobs:

| Credential | Used for |
| --- | --- |
| Your local SSH key for the VM | `ssh machinist` from your computer |
| A new GitHub SSH key created on the VM | Git clone, fetch, and push from the VM |
| GitHub CLI authentication on the VM | Issues, pull requests, comments, checks, and releases |
| Codex and Claude authentication on the VM | Running the coding-agent executors |

Do not copy private keys or Codex, Claude, or GitHub credential files from your
computer. Create and authenticate each VM credential on the VM itself.

## 1. Add the local SSH alias

On your computer, add this entry to `~/.ssh/config`:

```sshconfig
Host machinist
  HostName <VM_IP_OR_HOSTNAME>
  User root
  IdentityFile ~/.ssh/id_ed25519
  IdentitiesOnly yes
```

Test it with `ssh machinist`. Use the actual host name, user, and local identity
file on another VM.

## 2. Bootstrap the VM

While connected to the VM, run:

```sh
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/v0.2.0/scripts/setup-vm.sh | \
  MACHINIST_VERSION=v0.2.0 bash
```

The script installs Git, GitHub CLI, Codex, Claude Code, and the pinned
Machinist release. It initializes `~/.machinist` without overwriting existing
configuration, then installs systemd services for the control plane and worker.
It enables and starts the control plane immediately. It enables the worker only
when a repository is already configured. Re-running the bootstrap reinstalls
that pinned release and restarts the applicable services. Change both version
values together when installing another release. Machinist's installer verifies
the release archive against its published SHA-256 checksum. Review remote
scripts before executing them when the repository or network is outside your
trust boundary.

Switch to the runtime account before authenticating tools, creating its GitHub
key, cloning repositories, or editing Machinist configuration:

```sh
su - machinist
```

### Migrating a root-based v0.1.x installation

The v0.2.0 bootstrap deliberately stops if it detects services or configuration
from the earlier root-based setup. It does not copy root-owned agent credentials
into the unprivileged account.

Move the old configuration and units into a recoverable backup before
bootstrapping:

```sh
systemctl disable --now machinist-worker.service machinist-control-plane.service
mkdir -p /root/machinist-v0.1-backup
mv /root/.machinist /root/machinist-v0.1-backup/config
for unit in machinist-worker.service machinist-control-plane.service; do
  if [ -f "/etc/systemd/system/$unit" ]; then
    mv "/etc/systemd/system/$unit" /root/machinist-v0.1-backup/
  fi
done
systemctl daemon-reload
```

Then run the pinned bootstrap command above. Recreate the configuration under
`/home/machinist/.machinist`, reauthenticate GitHub, Codex, and Claude as the
`machinist` user, and clone repositories under `/home/machinist`. Keep the
backup until the new worker passes the smoke test.

To roll back before then, stop the new services, move the saved configuration
back to `/root/.machinist`, restore both saved unit files under
`/etc/systemd/system`, run `systemctl daemon-reload`, and enable and start the
old services again.

Verify the tools:

```sh
git --version
gh --version
codex --version
claude --version
machinist version
```

## 3. Create a dedicated GitHub SSH key on the VM

Create a key used only by this worker:

```sh
ssh-keygen -t ed25519 \
  -C "machinist@$(hostname)" \
  -f ~/.ssh/id_ed25519_machinist
```

An autonomous worker cannot unlock an interactive passphrase after a restart.
For a dedicated worker, leave the passphrase empty and protect the key through
host access, file permissions, and limited GitHub permissions. Use a passphrase
and persistent SSH agent only when you have designed that operational flow.

Protect the private key and print only the public key:

```sh
chmod 700 ~/.ssh
chmod 600 ~/.ssh/id_ed25519_machinist
chmod 644 ~/.ssh/id_ed25519_machinist.pub
cat ~/.ssh/id_ed25519_machinist.pub
```

Never print, copy, or upload `~/.ssh/id_ed25519_machinist` without the `.pub`
suffix. That file is the private key.

## 4. Add the public key to GitHub

In GitHub, open **Settings → SSH and GPG keys → New SSH key**.

1. Use a descriptive title such as `machinist-vm`.
2. Select **Authentication Key**.
3. Paste the complete `.pub` line printed by the previous command.
4. Save the key.

An account SSH key can access every repository the GitHub account permits. For
tighter isolation, use a dedicated GitHub machine account or repository deploy
keys. A deploy key is repository-specific and needs write access when the agent
must push branches.

Add this to `~/.ssh/config` on the VM:

```sshconfig
Host github.com
  HostName github.com
  User git
  IdentityFile ~/.ssh/id_ed25519_machinist
  IdentitiesOnly yes
```

Protect and test the configuration:

```sh
chmod 600 ~/.ssh/config
ssh -T git@github.com
```

On the first connection, compare the displayed host fingerprint with GitHub's
published SSH fingerprints before accepting it. A successful test says GitHub
authenticated the account and notes that shell access is unavailable.

## 5. Log in to GitHub CLI

SSH authentication and GitHub CLI authentication are separate. The shipped
agents use both Git and `gh`, so complete both:

```sh
gh auth login --hostname github.com --git-protocol ssh --web
```

Follow the device or browser instructions. Then verify:

```sh
gh auth status
gh api user --jq .login
```

Use a GitHub identity with only the repository access the worker needs.

## 6. Log in to Codex and Claude Code

Run each CLI interactively on the VM and follow its sign-in flow:

```sh
codex
claude
```

The official OpenAI documentation says the first `codex` run offers the
available sign-in methods. Do not copy a local credential store to the VM.

Verify the executors are visible to non-interactive processes:

```sh
command -v codex
command -v claude
```

The root bootstrap exposes both launchers through `/usr/local/bin` for workers
and services.

## 7. Clone and register repositories

Clone each repository the worker may use:

```sh
mkdir -p ~/Code/github/owainlewis
git clone git@github.com:owainlewis/machinist.git \
  ~/Code/github/owainlewis/machinist
```

Confirm Git can access the repository:

```sh
git -C ~/Code/github/owainlewis/machinist remote -v
git -C ~/Code/github/owainlewis/machinist fetch --dry-run
```

Register the checkout in `~/.machinist/worker.toml`:

```toml
[repositories.machinist]
path = "~/Code/github/owainlewis/machinist"
```

Add one entry for every repository and use a distinct logical name inside the
brackets.

Enable and start the worker now that it has a repository:

```sh
machinist validate
exit
systemctl enable --now machinist-worker.service
```

## 8. Run Machinist as a service

The bootstrap installs both services and enables the control plane. It enables
the worker after a repository is configured:

```sh
systemctl status machinist-control-plane.service
systemctl status machinist-worker.service
```

The control plane starts during bootstrap. A fresh bootstrap leaves the worker
disabled until you register a repository and enable it in the previous step.
The control plane remains bound to `127.0.0.1:7331`, and the worker connects to
it locally.

Follow their logs:

```sh
journalctl -u machinist-control-plane.service -f
journalctl -u machinist-worker.service -f
```

After changing `/home/machinist/.machinist/config.toml` or
`/home/machinist/.machinist/worker.toml`, restart both:

```sh
su - machinist
machinist validate
exit
systemctl restart machinist-control-plane.service machinist-worker.service
```

Stop or start the complete deployment:

```sh
systemctl stop machinist-worker.service machinist-control-plane.service
systemctl start machinist-control-plane.service machinist-worker.service
```

From your computer, open an SSH tunnel:

```sh
ssh -N -L 7331:127.0.0.1:7331 machinist
```

Open <http://127.0.0.1:7331>. Do not expose port 7331 directly or place the
current unauthenticated UI behind a public reverse proxy.

## 9. Run a smoke test

Verify the configuration and credentials before assigning a real issue:

```sh
systemctl is-active machinist-control-plane.service machinist-worker.service
su - machinist
machinist version
machinist validate
gh auth status
ssh -T git@github.com
command -v codex
command -v claude
```

Then run the shipped read-only audit agent against a narrow area:

```sh
machinist run \
  --command=audit \
  --repo=~/Code/github/owainlewis/machinist \
  --prompt="Inspect one narrow package and report only proven bugs"
exit
```

The audit agent is read-only by instruction, but the coding CLI still has the
operating-system and network permissions of the VM account.

## 10. Update Machinist

Install the latest stable release:

```sh
machinist update
```

Install a specific release for rollback or reproducibility:

```sh
machinist update --version v0.1.1
```

Machinist replaces the executable only after the matching release archive
passes SHA-256 verification.

Restart the services so they run the updated binary:

```sh
systemctl restart machinist-control-plane.service machinist-worker.service
```

## Optional agent prompt

Give this prompt to an agent that already has SSH access to the VM. It performs
non-interactive setup and stops for steps that require you:

```text
Set up Machinist on the remote Ubuntu or Debian VM available through
`ssh machinist`.

Follow docs/vm-deployment.md from the Machinist repository. You may install
system packages, run the official bootstrap, create a dedicated GitHub SSH key,
configure SSH, clone repositories after authentication, edit worker.toml, and
run verification commands.

Safety requirements:
- Never print, copy, or transmit a private SSH key.
- Never copy Codex, Claude, or GitHub credential files from another machine.
- Keep the Machinist control plane bound to 127.0.0.1.
- Do not expose port 7331 publicly.
- Do not weaken SSH host-key verification.
- Do not start real agent work or modify a GitHub repository.

Stop when SSH-key registration, a browser or device code, Codex login, or Claude
login requires me. Tell me exactly what I need to do, then wait. After I confirm
completion, resume the guide, verify every credential without revealing it, and
report any remaining manual steps.
```

## Troubleshooting

### `Permission denied (publickey)` from GitHub

Check that the public key is registered, the private key has mode `0600`, and
SSH selects it:

```sh
ssh -vT git@github.com
```

### `gh auth status` reports no account

Run `gh auth login --hostname github.com --git-protocol ssh --web` again. GitHub
SSH access does not automatically authenticate GitHub CLI.

### Machinist cannot find `codex` or `claude`

```sh
ls -l /usr/local/bin/codex /usr/local/bin/claude
command -v codex claude
```

Re-run the bootstrap if either launcher is missing.

### The browser cannot reach Machinist

Check the service and keep the SSH tunnel running on your computer:

```sh
systemctl status machinist-control-plane.service
ss -ltnp | grep 7331
```

The listener should be `127.0.0.1:7331`, not `0.0.0.0:7331`.

## Runtime security boundary

The bootstrap creates the unprivileged `machinist` account and the supplied
services run as that account. Its repositories, configuration, SSH keys, and
Codex, Claude, and GitHub credentials must remain under `/home/machinist`.
Do not give this account passwordless sudo or access to unrelated host data.
