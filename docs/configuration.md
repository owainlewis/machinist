# Configuration

There are three places to configure Machinist: [factory files](factory.md), the control
plane's `config.toml` and each runner's `runner.toml`. An unknown key stops startup and
names the key.

Machinist reads `config.toml` or `runner.toml` from the current directory, or the file
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
  config.toml          the control plane
  runner.toml          a runner
  factory/presets/     presets
  .machinist/          state, ignored by git
```

## Control plane: config.toml

The control plane reads its GitHub token from `GH_TOKEN`. It must belong to `github.handle`,
or startup stops.

```toml
[server]
listen = "127.0.0.1:7331"
database = ".machinist/tickets.db"
factory = "factory"
default_preset = "default"
max_turns_without_human = 5
run_timeout = "60m"
# tls_cert = "/etc/machinist/tls.crt"
# tls_key = "/etc/machinist/tls.key"

[github]
handle = "machinist-bot"
repositories = ["owner/name"]
```

| Key | Meaning |
| --- | --- |
| `listen` | Address for the UI, API and runners. Keep it private |
| `database` | The SQLite file. Back this up |
| `factory` | The factory directory |
| `default_preset` | The preset a job uses when it names none |
| `max_turns_without_human` | Runs allowed since a person last said something |
| `run_timeout` | The deadline for one run, when its preset sets none |
| `tls_cert`, `tls_key` | Serve HTTPS, and mark the sign-in cookie Secure |
| `github.handle` | The bot's login. Use a dedicated account |
| `github.repositories` | The repositories Machinist serves |

## Runner: runner.toml

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
machinist token create <name>
```

```sh
machinist token list
```

```sh
machinist token revoke <name>
```

These open the database directly, so they work before the server has started. There is one
kind of token, used by people and runners alike. Revoking one ends its sign-ins.

## API

Every route needs `Authorization: Bearer <token>` or the sign-in cookie, except `GET /healthz`,
`POST /api/v1/session` and static assets. See the
[design](designs/machinist-redesign.md#access) for the routes.
