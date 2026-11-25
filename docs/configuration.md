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
```

| Key | Meaning |
| --- | --- |
| `listen` | Address for the UI, API and runners. Keep it private. On a loopback address, requests whose Host is not `localhost`, `127.0.0.1` or `[::1]` get 421, which blocks DNS rebinding |
| `database` | The SQLite file. Back this up |
| `factory` | The factory directory |
| `default_preset` | The preset a job uses when it names none |
| `run_timeout` | The deadline for one run, when its preset sets none |
| `tls_cert`, `tls_key` | Serve HTTPS, and mark the sign-in cookie Secure |
| `github.handle` | The bot's login. Use a dedicated account. With a GitHub App, runs commit as the app's bot account instead |
| `github.repositories` | Repositories Machinist works on. You can also add them on the Settings > Projects page, with no restart. Automations can only name repositories listed here; `linear.projects` and `linear.teams` can also name repositories added on Settings > Projects |
| `github.auto_merge` | Controls auto-merge for pull requests. On (`true`) by default: the control plane auto-merges low-risk pull requests once checks pass, `review` accepted the change, and the default preset's `merge-gate` step approved them as `low` risk (medium- and high-risk pull requests stay open for a person). Set to `false` to run in **observe mode**: `merge-gate` still evaluates every pull request and posts its judgment comment without merging. Steps see it as `MACHINIST_AUTO_MERGE` |
| `github.app.id` | The [GitHub App](github-app.md)'s App ID. Set it to give each run a short-lived token |
| `github.app.private_key_file` | The app's private key file. Keep it out of git |
| `linear.*` | Start jobs from Linear issues. Off unless `linear.projects` or `linear.teams` is set. See [Linear](linear.md) |

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
| `kubernetes.*` | Makes this a Kubernetes runner |

## Tokens

```sh
machinist token create <name>            # for a person: the web app, the API and the CLI
machinist token create <name> --runner   # for a runner
```

```sh
machinist token list
```

```sh
machinist token revoke <name>
```

These open the database directly, so they work before the server has started. Revoking a
token ends its sign-ins.

A person's token can do everything except take runs. A runner's token can poll for runs and
report on the runs it was given, and gets 403 anywhere else, so it cannot read other jobs,
start work or sign in. One runner cannot report on another's run. `init` makes the `local`
runner a runner's token. A token made before tokens had roles shows as `any` in
`machinist token list` and can do both. Replace it with one of each.

## API

Every route needs `Authorization: Bearer <token>` or the sign-in cookie, except `GET /healthz`,
`POST /api/v1/session` and static assets. See the
[design](designs/machinist-redesign.md#access) for the routes.
