# Machinist

A job runner for coding agents. Your team assigns work in one place, and every job runs the
same predefined steps on machines you control: a laptop, a VM or Kubernetes. Everyone can
see what is running and what is waiting on a person. It is early-access, open-source
software.

- **One place for the team.** One control plane holds every job. Anyone signed in can see
  what each job is doing, step by step, and open its pull request.
- **Runs anywhere.** A runner is a small daemon that asks the control plane for work. Start
  one on a Mac, a VM or a Kubernetes cluster. The control plane treats them all the same.
- **A workflow you define.** A preset is a list of shell steps: clone, plan, build, test,
  review, open a pull request. A step can run Claude Code, Codex or any command. A test
  step is a gate an agent cannot skip.
- **Any trigger.** A tracker, a schedule or `curl` starts a job with one request.

Send the work to the [run API](docs/run-api.md), or let an automation start it. The job works on its own branch, and the default preset opens one pull request.
When a step needs a decision, it asks and the job waits.

Machinist is moving to one generic workflow runner. See [Direction](docs/direction.md) for where
it is going and how far the code has got.

<a id="quick-start"></a>

## Quick start

This runs the control plane and one runner on your machine. You need Go 1.26.6, Git, the
GitHub CLI signed in with `gh auth login`, and Claude Code signed in.

```sh
git clone https://github.com/owainlewis/machinist.git
cd machinist
go build -o ./bin/machinist ./cmd/machinist
./bin/machinist init
```

`init` creates `~/.machinist` with a control plane config, a runner config, four presets
and a token for a local runner.

1.  In `~/.machinist/config.toml`, set `handle` to the GitHub login that owns your token,
    which `gh api user --jq .login` prints, and list the repositories jobs may work on.
    Commits are made under that handle:

    ```toml
    handle = "your-login"
    repositories = ["owner/name"]
    ```

2.  In `~/.machinist/runner.toml`, set `env = []` and uncomment `login = true`, so steps
    use your own Claude Code login. To use an API key instead, keep
    `env = ["ANTHROPIC_API_KEY"]` and set it in the runner's environment. See
    [Credentials](docs/credentials.md).
3.  Start the control plane in one terminal and the runner in another:

    ```sh
    GH_TOKEN=$(gh auth token) ./bin/machinist start
    ```

    ```sh
    GH_TOKEN=$(gh auth token) ./bin/machinist runner start
    ```

4.  Create a token for yourself:

    ```sh
    TOKEN=$(./bin/machinist token create "$USER" | tail -n 1)
    ```

5.  Start a job with the `hello` preset. It clones nothing and runs no agent, so it proves
    the runner works in a few seconds:

    ```sh
    curl -sS -X POST http://127.0.0.1:7331/machinist/run \
      -H "Authorization: Bearer $TOKEN" \
      -d '{"key":"hello-1","preset":"hello","vars":{"repo":"owner/name"},"prompt":"Say hello"}'
    ```

6.  Open `http://127.0.0.1:7331`, sign in with the token and open the job. It should be
    Done, with each step's output.
7.  Now give it real work with the `coding` preset. It opens a draft pull request:

    ```sh
    curl -sS -X POST http://127.0.0.1:7331/machinist/run \
      -H "Authorization: Bearer $TOKEN" \
      -d '{"key":"fix-1","preset":"coding","vars":{"repo":"owner/name"},"prompt":"Fix the toast position"}'
    ```

The pull request comes from whoever owns `GH_TOKEN`. For a team, give the control plane
and runners a bot account's token instead, and set `handle` to its login.

The production UI is committed, so this build does not need Node.

## Using it

Jobs start through the [run API](docs/run-api.md), from `curl`, a tracker, a webhook or an
automation. The web app is read-only:

| Page | What it does |
| --- | --- |
| Jobs | The newest jobs, with each one's repository, preset, pull request and state |
| Job | One job: its question if it is waiting, its result, its prompt, and every attempt with the output of each step |
| Runners | Each runner, where it runs, how many of its slots are busy and with what, and how long runs have waited |

To answer a job's question, `POST /machinist/jobs/{id}/input`. To stop it,
`POST /machinist/jobs/{id}/cancel`.

## Testing Machinist itself

```sh
go test ./...
cd internal/controlplane/web && npm ci && npm test
```

The Go tests run whole jobs end to end with fake `claude` and `gh` commands and a local
Git remote. See [Development](docs/development.md).

Machinist never merges a pull request unless you add an automation that uses a preset allowed
to. See [Security](SECURITY.md) for what a step can do.

## How it fits together

One control plane holds jobs and presets. It never runs a step. Any number
of runners, on Macs, VMs or Kubernetes, dial out and ask for work. See
[Architecture](ARCHITECTURE.md).

## Go deeper

| Guide | What it covers |
| --- | --- |
| [Documentation](docs/README.md) | The full list |
| [Direction](docs/direction.md) | What Machinist is for, and what is missing |
| [Run API](docs/run-api.md) | Starting jobs from any system |
| [Factory files](docs/factory.md) | Presets and automations |
| [Runners](docs/runners.md) | Adding local and Kubernetes runners |
| [Credentials](docs/credentials.md) | Which credential to use, and why |
| [Configuration](docs/configuration.md) | Every setting |
| [Development](docs/development.md) | Build, test and contribute |

Read [CONTRIBUTING.md](CONTRIBUTING.md) before contributing. Machinist uses the
[MIT License](LICENSE).
