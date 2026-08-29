# Deploy Machinist on a VM

The supported VM layout runs a published Machinist binary and keeps source
checkouts only for repositories that coding agents work on. The VM does not need
Go, Node.js, or a clone of the Machinist repository at runtime.

## Bootstrap Ubuntu or Debian

Connect to the VM and run the bootstrap script:

```sh
ssh machinist
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/main/scripts/setup-vm.sh | bash
```

The script installs Git, GitHub CLI, Codex, Claude Code, and the latest published
Machinist release. It then creates the default configuration under
`~/.machinist`. Codex and Claude Code remain installed under `~/.local/bin`; the
root bootstrap links their launchers into `/usr/local/bin` so non-interactive
workers and services can find them. Review remote scripts before piping them
into a shell when the repository or network is outside your trust boundary.

Authenticate each tool interactively on the VM:

```sh
gh auth login
codex
claude
```

Do not copy local Codex, Claude, or GitHub credential files to the VM.

## Install or update Machinist

Install the latest stable release without the full VM bootstrap:

```sh
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/main/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
```

Install a specific release:

```sh
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/main/install.sh |
  MACHINIST_VERSION=v0.2.0 sh
```

An installed binary can update itself. The update is checksum-verified and the
current executable is replaced only after the new archive passes validation:

```sh
machinist update
machinist update --version v0.2.0
```

When Machinist is installed in a root-owned directory, run the update with the
matching account or reinstall into a user-writable directory:

```sh
curl -fsSL https://raw.githubusercontent.com/owainlewis/machinist/main/install.sh |
  MACHINIST_INSTALL_DIR="$HOME/.local/bin" sh
```

## Clone agent repositories

Clone every repository the worker may change. A clone is required for agent
work, but Machinist itself still runs from the release binary:

```sh
mkdir -p ~/Code/github/owainlewis
gh repo clone owainlewis/machinist ~/Code/github/owainlewis/machinist
```

Register each checkout in `~/.machinist/worker.toml`:

```toml
[repositories.machinist]
path = "~/Code/github/owainlewis/machinist"
```

## Run and connect

Start the control plane and worker in separate terminals:

```sh
machinist start
```

```sh
machinist worker start
```

The current control plane intentionally listens only on loopback. From your
computer, create an SSH tunnel:

```sh
ssh -N -L 7331:127.0.0.1:7331 machinist
```

Then open <http://127.0.0.1:7331>. Do not expose port 7331 directly or place the
current unauthenticated UI behind a public reverse proxy.

The bootstrap command above runs as root and configures root as the Machinist
runtime account. Treat that VM as a dedicated, privileged coding worker. For a
long-running deployment, run both processes under a service manager as the same
account that owns the agent credentials, Machinist configuration, and repository
checkouts.

To use a non-root runtime account instead, create that account first, run the
Codex, Claude Code, and Machinist installers plus `machinist init` while logged in
as that account, and keep its repositories and credentials under its home
directory. Do not mix configuration or credentials between root and a service
account.
