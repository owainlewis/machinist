# Run Machinist on a VM

A VM can host the control plane and a runner, or only a runner. Use a dedicated unprivileged
account on a trusted Linux VM. Nothing on the VM needs to be reachable from the internet:
the control plane listens on `127.0.0.1`, you reach the UI over SSH, and Machinist polls
GitHub and Linear rather than taking webhooks.

## Set up with the script

As root, on Ubuntu or Debian, with the release you want:

```sh
MACHINIST_VERSION=v0.2.0 bash scripts/setup-vm.sh
```

It installs Machinist, Git, the GitHub CLI, Claude Code and Codex, runs `machinist init` as the
`machinist` user in `/home/machinist`, and installs the systemd services, which run from that
directory. It prints the remaining steps. To keep the config in git, make `/home/machinist`
a checkout of your repository before running the script; `init` keeps files that exist.

## Or build from source

To run code from `main` that is not in a release yet, build it on the VM instead. As root,
with Go installed:

```sh
apt-get install -y ca-certificates curl git gh
git clone https://github.com/owainlewis/machinist.git /usr/local/src/machinist
cd /usr/local/src/machinist
go build -o /usr/local/bin/machinist ./cmd/machinist
useradd --create-home --shell /bin/bash machinist
runuser -u machinist -- env HOME=/home/machinist bash -c 'curl -fsSL https://claude.ai/install.sh | bash'
ln -sfn /home/machinist/.local/bin/claude /usr/local/bin/claude
runuser -u machinist -- env HOME=/home/machinist bash -c 'cd && machinist init'
install -d -m 0700 /etc/machinist
install -m 0600 /dev/null /etc/machinist/control-plane.env
install -m 0600 /dev/null /etc/machinist/runner.env
install -m 0644 deploy/systemd/machinist-control-plane.service deploy/systemd/machinist-runner.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now machinist-control-plane
```

To update later, pull, build and restart both services:

```sh
cd /usr/local/src/machinist && git pull --ff-only && go build -o /usr/local/bin/machinist ./cmd/machinist
systemctl restart machinist-control-plane machinist-runner
```

## Configure it

As the `machinist` user (`su - machinist`), in `/home/machinist`:

1.  In `config.yaml`, set `github.handle` and list the repositories jobs may work on in
    `github.repositories`.
2.  Set up a [GitHub App](github-app.md), so each run gets a token for its own repository
    that lasts an hour. Put its key at `/home/machinist/.machinist/github-app.pem` and add
    `github.app` to `config.yaml`.
3.  Choose how the agent signs in. To use a Claude login, set `env: []` and `login: true`
    in `runner.yaml`, then run `claude` once as the `machinist` user and sign in. To use an
    API key instead, keep `env: [ANTHROPIC_API_KEY]` and put the key in
    `/etc/machinist/runner.env`. See [Credentials](credentials.md).

Then, as root, start the runner and check both services:

```sh
systemctl restart machinist-control-plane
systemctl enable --now machinist-runner
systemctl status machinist-control-plane machinist-runner
```

## Credentials

Secrets go in two files that only root can read. systemd passes them to the services.
Neither needs a GitHub token when a GitHub App is set.

| File | Holds |
| --- | --- |
| `/etc/machinist/control-plane.env` | `GH_TOKEN` for the bot, only without a GitHub App. `LINEAR_CLIENT_ID` and `LINEAR_CLIENT_SECRET`, to [start jobs from Linear](linear.md#set-it-up) |
| `/etc/machinist/runner.env` | The variables listed in `env` in `runner.yaml`, such as `ANTHROPIC_API_KEY`. `GH_TOKEN` only without a GitHub App |

See [Credentials](credentials.md).

## Reach the UI

Keep the control plane on `127.0.0.1` and forward its port over SSH. On your own computer,
run this and leave it open:

```sh
ssh -N -L 7331:127.0.0.1:7331 USER@VM_HOST
```

`-N` runs no command on the VM, and `-L` sends `127.0.0.1:7331` on your computer to the
same address on the VM. To make it short, add the VM to `~/.ssh/config`:

```text
Host machinist
  HostName VM_HOST
  User root
  LocalForward 7331 127.0.0.1:7331
```

Then `ssh -N machinist` opens the tunnel.

Create a token for yourself on the VM. It prints once, so copy it:

```sh
su - machinist -c 'machinist token create your-name'
```

Open `http://127.0.0.1:7331` and sign in with the token.

## Check it works

- The **Runners** page shows the runner, with its free slots.
- `systemctl status machinist-control-plane machinist-runner` shows both as active.
- `journalctl -u machinist-runner -f` follows the runner as it works, and
  `journalctl -u machinist-control-plane -f` follows the control plane.

For a first real job, select **New job**, pick a repository and give it a small task, such
as adding a section to the README. The job page shows each step as it runs, and the job
ends with a pull request, which merges once its checks pass when `github.auto_merge` is on. If it fails, the page shows which step failed and why.

## Move an old install

An install from before this layout keeps TOML config in `~/.machinist`. To move it:

1.  Run `machinist init` in `/home/machinist` for the new files.
2.  Copy your settings from `~/.machinist/config.toml` and `runner.toml` into
    `config.yaml` and `runner.yaml`, and your presets into `factory/presets/*.yaml`.
3.  Stop both services. Move `~/.machinist/server/tickets.db` to `.machinist/tickets.db`
    and `~/.machinist/runner.token` to `.machinist/runner.token`, replacing the new ones,
    so job history and tokens carry over.
4.  Start both services.

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

Then check the runner's token. A token made before tokens had roles shows the role `any`.
It still takes runs, but it can also sign in to the web app and use the API. List the tokens:

```sh
su - machinist -c 'machinist token list'
```

If the runner's token shows `any`, give the runner a token with the runner role. Write it to
the runner's token file, restart the runner, then revoke the old token by its name. Here it
is `local`:

```sh
su - machinist -c 'umask 077; machinist token create local-runner --runner > .machinist/runner.token'
systemctl restart machinist-runner
su - machinist -c 'machinist token revoke local'
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
