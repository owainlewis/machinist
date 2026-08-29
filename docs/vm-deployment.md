# Set up Machinist on a remote VM

This guide sets up a dedicated Ubuntu or Debian VM that runs Machinist with
Codex and Claude Code. Machinist runs from a published binary. Git checkouts are
needed only for repositories that coding agents may inspect or change.

The examples use a local SSH alias named `machinist` and configure root as the
runtime account. Treat this as a dedicated, privileged coding worker.

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
  HostName 89.167.49.125
  User root
  IdentityFile ~/.ssh/id_ed25519
  IdentitiesOnly yes
```

Test it with `ssh machinist`. Use the actual host name, user, and local identity
file on another VM.

## 2. Bootstrap the VM

While connected to the VM, run:

```sh
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/main/scripts/setup-vm.sh | bash
```

The script installs Git, GitHub CLI, Codex, Claude Code, and the latest stable
Machinist release. It initializes `~/.machinist` without overwriting existing
configuration, then installs and starts systemd services for the control plane
and worker. Review remote scripts before executing them when the repository or
network is outside your trust boundary.

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

## 8. Run Machinist as a service

The bootstrap installs and enables these services:

```sh
systemctl status machinist-control-plane.service
systemctl status machinist-worker.service
```

They start automatically after a reboot. The control plane remains bound to
`127.0.0.1:7331`, and the worker connects to it locally.

Follow their logs:

```sh
journalctl -u machinist-control-plane.service -f
journalctl -u machinist-worker.service -f
```

After changing `~/.machinist/config.toml` or `worker.toml`, restart both:

```sh
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
machinist version
machinist update
systemctl is-active machinist-control-plane.service machinist-worker.service
gh auth status
ssh -T git@github.com
command -v codex
command -v claude
```

Then run the shipped read-only audit agent against a narrow area:

```sh
machinist run \
  --agent=audit \
  --repo=~/Code/github/owainlewis/machinist \
  --prompt="Inspect one narrow package and report only proven bugs"
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

## Non-root alternative

Create the runtime account first. Run the Codex, Claude Code, and Machinist
installers, SSH-key setup, GitHub CLI authentication, and `machinist init` while
logged in as that account. Keep its repositories and credentials under its home
directory. Run the control plane and worker as that same account. Do not mix
configuration or credentials between accounts.
