# Run Machinist on a VM

A VM can host the control plane and a runner, or only a runner. Use a dedicated unprivileged
account on a trusted Linux VM.

## Set up with the script

As root, on Ubuntu or Debian, with the release you want:

```sh
MACHINIST_VERSION=v0.2.0 bash scripts/setup-vm.sh
```

It installs Machinist, Git, the GitHub CLI, Claude Code and Codex, runs `machinist init` as the
`machinist` user in `/home/machinist`, and installs the systemd services, which run from that
directory. It prints the remaining steps. To keep the config in git, make `/home/machinist`
a checkout of your repository before running the script; `init` keeps files that exist.

An install from before this layout keeps TOML config in `~/.machinist`. To move it:

1.  Run `machinist init` in `/home/machinist` for the new files.
2.  Copy your settings from `~/.machinist/config.toml` and `runner.toml` into
    `config.yaml` and `runner.yaml`, and your presets into `factory/presets/*.yaml`.
3.  Stop both services. Move `~/.machinist/server/tickets.db` to `.machinist/tickets.db`
    and `~/.machinist/runner.token` to `.machinist/runner.token`, replacing the new ones,
    so job history and tokens carry over.
4.  Start both services.

## Credentials

Secrets go in two files that only root can read. systemd passes them to the services.

| File | Holds |
| --- | --- |
| `/etc/machinist/control-plane.env` | `GH_TOKEN` for the bot |
| `/etc/machinist/runner.env` | `GH_TOKEN` for the bot and the variables in `env` |

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
On the other machine, set `control_plane` and `token_file` in `runner.yaml`. See
[Runners](runners.md). The runner dials out, so it needs no inbound route.

## Upgrade

As root, keep the old binary and install the new one in its place with the same owner and mode:

```sh
cp -p /usr/local/bin/machinist /usr/local/bin/machinist.previous
install -o root -g root -m 0755 ./machinist /usr/local/bin/machinist
```

When no job is running, restart both services and check that `/healthz` reports the new version:

```sh
systemctl restart machinist-control-plane machinist-runner
curl -fsS http://127.0.0.1:7331/healthz
```

To roll back, install the previous binary the same way and restart both:

```sh
install -o root -g root -m 0755 /usr/local/bin/machinist.previous /usr/local/bin/machinist
systemctl restart machinist-control-plane machinist-runner
```

## Back up

Back up `.machinist/tickets.db` with a consistent SQLite copy, and your factory
directory. GitHub holds branches and pull requests, but not ticket history or the ledger.
Run one control plane per database.
