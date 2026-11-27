# Configuration

There are three places to configure Machinist: [factory files](factory.md), the control
plane's `config.yaml` and each runner's `runner.yaml`. All of them are YAML. An unknown key
stops startup and names the key and its line. A file left over in TOML stops startup too,
and says to rewrite it as YAML.

Machinist reads `config.yaml` or `runner.yaml` from the current directory, or the file
`--config` names. Keep that directory in git with the factory, and start Machinist from it.
`machinist init [directory]` writes commented copies there.

Relative paths in either file are read from the file's own directory. By default
everything Machinist writes goes under `.machinist/` beside the config: the database, the
runner token and run workspaces. `init` adds `.machinist/` to `.gitignore`. Point
`database` or `data_directory` elsewhere, such as `/var/lib/machinist`, to keep state off
the checkout. Secrets never go in these files: tokens come from the environment or a token
file.

```text
my-machinist/          a git repository
  config.yaml          the control plane
  runner.yaml          a runner
  factory/presets/     presets (supports extends and harness/model overrides)
  factory/agents/      agents
  factory/prompts/     prompts
  factory/scripts/     scripts
  .machinist/          state, ignored by git
```

Presets in `factory/presets/` can inherit from another preset with `extends: <preset>` and
override `harness` and `model` at the preset or step level without duplicating agents or
scripts. See [Extending presets and overriding harness or model](factory.md#extending-presets-and-overriding-harness-or-model).

## Control plane: config.yaml

The control plane reads its GitHub token from `GH_TOKEN`, unless `github.app` is set. Then
it makes short-lived tokens from the [GitHub App](github-app.md) for its own calls and for
each run, and needs no `GH_TOKEN`.

```yaml
server:
  listen: 127.0.0.1:7331
  # public_url: https://machinist.example.com
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 60m
  # tls_cert: /etc/machinist/tls.crt
  # tls_key: /etc/machinist/tls.key

github:
  handle: machinist-bot
  repositories: [owner/name]
  auto_merge: true
  steward_retries: 2

# notify:
#   webhook: https://hooks.slack.com/services/...
```

| Key | Meaning |
| --- | --- |
| `listen` | Address for the UI, API and runners. Defaults to `127.0.0.1:7331` (or `:$PORT` when `PORT` is set on Cloud Run or with `public_url`). On loopback, requests whose Host is not `localhost`, `127.0.0.1` or `[::1]` get 421, blocking DNS rebinding |
| `public_url` | Base URL of the web app, such as `https://machinist.example.com`. Used for GitHub OAuth callbacks, invite links, and job links in webhook posts (`<public_url>/#/jobs/<id>`) |
| `trusted_proxy_hops` | Number of reverse proxies in front of the control plane (`0` to `10`, default `0`). Set `1` on Cloud Run, or `2` behind an HTTPS Load Balancer |
| `database` | SQLite file path (default `.machinist/tickets.db`) or a PostgreSQL URL (`postgres://` or `postgresql://`). Overridden by the `DATABASE_URL` environment variable. Back this up |
| `factory` | The factory directory |
| `default_preset` | The preset a job uses when it names none |
| `run_timeout` | The deadline for one run, when its preset sets none |
| `tls_cert`, `tls_key` | Serve HTTPS, and mark the sign-in cookie Secure |
| `auth.github.enabled` | Turn on GitHub OAuth sign-in for people. Needs `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET` in the environment |
| `auth.disable_token_signin` | Turn off token sign-in at `POST /api/v1/session`. Defaults to `true` when `auth.github.enabled` is `true`, and `false` otherwise |
| `auth.allow_token_only_public` | Allow token-only sign-in when `public_url` is not loopback. Defaults to `false` |
| `auth.bootstrap_admin` | First admin account in `github:<login>` format. Overridden by `MACHINIST_BOOTSTRAP_ADMIN`. Inert after the first admin signs in |
| `github.handle` | The bot's login. Use a dedicated account. With a GitHub App, runs commit as the app's bot account instead |
| `github.repositories` | Repositories Machinist works on. You can also add them on Settings → Projects, with no restart. File-backed automations name repositories listed here; UI-managed automations and `linear.projects` / `linear.teams` can also use repositories added on Settings → Projects |
| `github.auto_merge` | Controls auto-merge for pull requests. On (`true`) by default: the control plane merges low-risk pull requests once checks pass, `review` accepted the change, and `merge-gate` approved them as `low` risk. Medium- and high-risk pull requests stay open for a person. Set `false` for **observe mode**: `merge-gate` comments its verdict without merging. Steps see it as `MACHINIST_AUTO_MERGE` |
| `github.steward_retries` | Maximum automatic **PR Steward** retries per job when an open pull request fails CI or hits a merge conflict (`DIRTY` or a conflicting branch update). Defaults to `2`; `0` disables; max `5`. Never runs if a person pushed commits after the review |
| `github.app.id` | The [GitHub App](github-app.md)'s App ID. Gives each run a short-lived token |
| `github.app.private_key_file` | The app's private key file. Keep it out of git |
| `linear.*` | Start jobs from Linear issues. Off unless `linear.projects` or `linear.teams` is set. See [Linear](linear.md) |
| `notify.webhook` | Optional HTTP or HTTPS URL (overridden by `NOTIFY_WEBHOOK_URL`) that gets one JSON `POST` when a job needs a person or fails. Sends a Slack-compatible `text` field plus `ticket_id`, `title`, `repo`, `state`, `created_by`, and `url` (when `server.public_url` is set). Prompts and agent output are never sent |

### Reloading without a restart

A restart is no longer needed after editing `config.yaml` or the factory directory. Run
`machinist reload` from the configuration directory (or pass `--config`) to ask the running
control plane to re-read `config.yaml` and the factory without interrupting queued or running
jobs:

```sh
machinist reload
```

`machinist reload` prints what changed. If anything in `config.yaml` or the factory is
invalid, the control plane rejects the reload, keeps what it had, and returns the error. Runs
already queued or running keep the job definition they started with; new jobs use the reloaded
configuration and factory.

To reload a remote control plane, pass `--url` and `--token` (or set `MACHINIST_URL` and
`MACHINIST_TOKEN`), or call `POST /api/v1/reload` with a full operator token.

These settings still need a control plane restart to take effect (when they differ during a
reload, `machinist reload` names them and does not apply them):

- `server.listen`
- `server.database`
- `server.tls_cert`
- `server.tls_key`

Environment variables read by the control plane process (`DATABASE_URL`, `GH_TOKEN`,
`GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`, `MACHINIST_BOOTSTRAP_ADMIN`,
`NOTIFY_WEBHOOK_URL`, `LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET`, `LINEAR_API_TOKEN`) also
need a restart when changed.

### Merge gate, observe mode and per-repository policy

The `default` preset runs a read-only `merge-gate` step after `publish` and before `merge` to
triage each pull request's blast radius before handing it to the control plane:

- **`github.auto_merge: true` (default):** Enables auto-merge for low-risk approved pull
  requests. The control plane merges a pull request only when CI checks pass, `review`
  accepted the change, and `merge-gate` approved it as `low` risk. Medium- and high-risk
  changes stay open for human review with the gate's judgment posted as a PR comment.
- **`github.auto_merge: false` (observe mode):** Runs `merge-gate` in **observe mode**. The
  gate still evaluates every pull request and posts its judgment comment (`approve` or
  `human_review`, risk level, and reason) on GitHub, while the control plane never merges the
  pull request. Use observe mode to validate and tune your merge policy before enabling
  auto-merge.
- **Per-repository override:** By default, `merge-gate` applies
  `factory/prompts/merge-policy.md`. Commit `.machinist/merge-policy.md` to a repository's
  default branch to override the policy for that repository. The gate reads it from
  `origin/HEAD` rather than the PR branch, so a pull request cannot weaken its own policy.

See [Merge gate and policy](factory.md#merge-gate-and-policy) for step details and
[Merge policies](merge-policies.md) for starter templates.

## Runner: runner.yaml

See [Runners](runners.md) for each type.

| Key | Meaning |
| --- | --- |
| `control_plane` | The control plane's address |
| `token_file` | A file holding this runner's token |
| `data_directory` | Workspaces and activity, removed after each run |
| `slots` | How many runs at once |
| `env` | Variables passed from the runner's environment to every step. See [Runners](runners.md#what-steps-can-use) |
| `login` | Give steps the OS user's home, so a tool can use its login. Local runners only |
| `docker.*` | Makes this a hardened Docker container runner (with optional `runtime: runsc` for gVisor) |
| `kubernetes.*` | Makes this a Kubernetes runner |

## Tokens, invites and users

```sh
machinist token create <name>                # full operator token: web app, API and MCP
machinist token create <name> --scope run    # scoped bot token: list, inspect, run and cancel jobs
machinist token create <name> --scope read   # read-only bot token: list and inspect jobs
machinist token create <name> --runner       # runner token: take runs and report on them
```

```sh
machinist token list
machinist token revoke <name>
```

When using GitHub OAuth sign-in, manage team invites and users with:

```sh
machinist invite create --github <login> --role member   # admin, member, or viewer
machinist invite list
machinist invite revoke <id>

machinist user list
machinist user set-role <id-or-login> <role>
machinist user disable <id-or-login>
machinist user enable <id-or-login>
```

These commands open the database directly (SQLite or `DATABASE_URL`), so they work before the
server starts. Revoking a token or disabling a user ends their active sessions.

A full operator token can do everything except take runs. A scoped token (`read` or `run`)
cannot sign in to the web app or manage tokens and projects. A runner token can poll for runs
and report on its own runs, and gets 403 anywhere else. `init` makes the `local` runner a
runner token. A token made before tokens had roles shows as `any` in `machinist token list`
and can do both; replace it with one of each.

## API

Every route needs `Authorization: Bearer <token>` or the sign-in cookie, except `GET /healthz`,
`POST /api/v1/session` and static assets. See the
[design](designs/machinist-redesign.md#access) for the routes.
