# Configuration

There are three places to configure Machinist: [factory files](factory.md), the control
plane's `config.toml` and each runner's `runner.toml`. `machinist init` installs commented
copies in `~/.machinist`. An unknown key stops startup and names the key.

## Control plane: config.toml

The control plane reads its GitHub token from `GH_TOKEN`. It must belong to `github.handle`,
or startup stops.

```toml
[server]
listen = "127.0.0.1:7331"
database = "~/.machinist/server/tickets.db"
factory = "~/.machinist/factory"
default_agent = "default"
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
| `listen` | Address for the UI, API, MCP and runners. Keep it private |
| `database` | The SQLite file. Back this up |
| `factory` | The factory directory |
| `default_agent` | The agent new tickets use |
| `max_turns_without_human` | Agent runs allowed since a person last said something |
| `run_timeout` | The deadline for one run |
| `tls_cert`, `tls_key` | Serve HTTPS, and mark the sign-in cookie Secure |
| `github.handle` | The bot's login. Use a dedicated account |
| `github.repositories` | The repositories Machinist serves |

## Runner: runner.toml

See [Runners](runners.md) for each type.

| Key | Meaning |
| --- | --- |
| `control_plane` | The control plane's address |
| `token_file` | A file holding this runner's token |
| `data_directory` | Clones and activity, removed after each run |
| `slots` | How many runs at once |
| `credentials.<harness>.env` | Variable names the harness reads. See [Credentials](credentials.md) |
| `credentials.<harness>.login` | Use the OS user's existing login. Local runners only |
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
