# Machinist

A job runner for coding agents. Your team assigns work in one place, and every job runs the
same predefined steps on machines you control: a laptop, a VM or Kubernetes. Everyone can
see what is running and what is waiting on a person. It is early-access, open-source
software.

- **One place for the team.** One control plane holds every job. Anyone signed in can see
  what each agent is doing, answer its questions and open its pull request.
- **Runs anywhere.** A runner is a small daemon that asks the control plane for work. Start
  one on a Mac, a VM or a Kubernetes cluster. The control plane treats them all the same.
- **A workflow you define.** A preset is a list of shell steps: clone, plan, build, test,
  review, open a pull request. A step can run Claude Code, Codex or any command. A test
  step is a gate an agent cannot skip.
- **Any trigger.** A tracker, a schedule or `curl` starts a job with one request.

Type the work into a box, send it to the [run API](docs/run-api.md), or let an automation
start it. The job works on its own branch, and the default preset opens one pull request.
When a step needs a decision, it asks and the job waits.

Machinist is moving to one generic workflow runner. See [Direction](docs/direction.md) for where
it is going and how far the code has got.

<a id="quick-start"></a>

## Quick start

You need Go 1.26.6, Git, the GitHub CLI and Claude Code. You also need a GitHub
account for the bot, with write access to the repositories it will work on.

```sh
git clone https://github.com/owainlewis/machinist.git
cd machinist
go build -o ./bin/machinist ./cmd/machinist
./bin/machinist init
```

`init` creates `~/.machinist` with a control plane config, a runner config, a starter
factory and a token for a local runner.

1.  In `~/.machinist/config.toml`, set `github.handle` to the bot's login and list your
    repositories.
2.  In `~/.machinist/runner.toml`, list the variables your steps need in `env`. See
    [Credentials](docs/credentials.md).
3.  Start the control plane with the bot's token:

    ```sh
    GH_TOKEN=<bot token> ./bin/machinist start
    ```

4.  With the bot's token and those variables set, prove the runner works, then
    start it:

    ```sh
    GH_TOKEN=<bot token> ANTHROPIC_API_KEY=<key> ./bin/machinist runner check --repo owner/name
    ```

    ```sh
    GH_TOKEN=<bot token> ANTHROPIC_API_KEY=<key> ./bin/machinist runner start
    ```

5.  Create a token for yourself and sign in at `http://127.0.0.1:7331`:

    ```sh
    ./bin/machinist token create "$USER"
    ```

The production UI is committed, so this build does not need Node.

## Using it

| Page | What it does |
| --- | --- |
| Home | One box to assign work: write it, or paste an issue number or link. Under it, what is running now, what needs you, the pass rate and tokens for the last 14 days, and a list of recent work where you can answer a question in place |
| Work | One table of jobs with a status on each row, and tabs to filter it. Open a job to follow it and reply, like a chat, with its details at the side |
| Automations | Your scheduled jobs, when each is next due and what it last did |
| Runners | Each runner, where it runs, how many of its slots are busy and with what, and how long runs have waited |

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
