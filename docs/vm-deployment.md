# Run Machinist on a VM

A VM can host the control plane and a runner, or only a runner. Use a dedicated unprivileged
account on a trusted Linux VM.

## Set up with the script

As root, on Ubuntu or Debian, with the release you want:

```sh
MACHINIST_VERSION=v0.2.0 bash scripts/setup-vm.sh
```

It installs Machinist, Git, the GitHub CLI and both harnesses, runs `machinist init` as the
`machinist` user, and installs the systemd services. It prints the remaining steps.

## Credentials

Secrets go in two files that only root can read. systemd passes them to the services.

| File | Holds |
| --- | --- |
| `/etc/machinist/control-plane.env` | `GH_TOKEN` for the bot |
| `/etc/machinist/runner.env` | `GH_TOKEN` for the bot and the harness credentials |

See [Credentials](credentials.md).

## Reach the UI

Keep the control plane on `127.0.0.1` and forward its port over SSH:

```sh
ssh -N -L 7331:127.0.0.1:7331 USER@VM_HOST
```

Create a token with `machinist token create <name>` as the `machinist` user, then open
`http://127.0.0.1:7331`.

## A runner on another machine

Set `listen` to a private address on the control plane, and create a token for the runner.
On the other machine, set `control_plane` and `token_file` in `runner.toml`. See
[Runners](runners.md). The runner dials out, so it needs no inbound route.

## Back up

Back up `~/.machinist/server/tickets.db` with a consistent SQLite copy, and your factory
directory. GitHub holds branches and pull requests, but not ticket history or the ledger.
Run one control plane per database.
