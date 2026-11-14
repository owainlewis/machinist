# Machinist

A job runner for coding agents. Your team assigns work in one place, and agents run it on
machines you control: a laptop, a VM or Kubernetes. Everyone can see what is running, what
is waiting on a person and what it cost. It is early-access, open-source software.

- **One place for the team.** One control plane holds every job. Anyone signed in can see
  what each agent is doing, answer its questions and open its pull request.
- **Runs anywhere.** A runner is a small daemon that asks the control plane for work. Start
  one on a Mac, a VM or a Kubernetes cluster. The control plane treats them all the same.
- **Your agents.** An agent is a folder of plain files: a playbook, a model, and the
  subagents and skills it can use. It runs on Claude Code or Codex.
- **A pipeline you define.** A job is a list of stages: an agent, another agent, a script.
  A script stage is a gate an agent cannot skip.
- **Usage you can see.** Runs, pass rate, tokens and cost for the last two weeks, and the
  figures for every job.

Mention the bot on a GitHub issue, type the work into a box, or send it from a coding
session over MCP. An agent works on its own branch and opens one pull request. A fresh
reviewer comments on it. If a person asks for changes or CI fails, the same job continues
on the same pull request. When the agent needs a decision, it asks and waits.

See [Direction](docs/direction.md) for where this is going and what is missing today.

<a id="quick-start"></a>

## Quick start

You need Go 1.26.6, Git, the GitHub CLI and Claude Code or Codex. You also need a GitHub
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
2.  In `~/.machinist/runner.toml`, choose a credential for each harness. See
    [Credentials](docs/credentials.md).
3.  Start the control plane with the bot's token:

    ```sh
    GH_TOKEN=<bot token> ./bin/machinist start
    ```

4.  With the bot's token and your harness credential set, prove the runner works, then
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

A mention counts only from someone with write access. Machinist never merges a pull
request unless you add an automation that uses an agent allowed to. See
[Security](SECURITY.md) for what an agent can do.

## How it fits together

One control plane holds tickets and talks to GitHub. It never starts an agent. Any number
of runners, on Macs, VMs or Kubernetes, dial out and ask for work. See
[Architecture](ARCHITECTURE.md).

## Go deeper

| Guide | What it covers |
| --- | --- |
| [Documentation](docs/README.md) | The full list |
| [Direction](docs/direction.md) | What Machinist is for, and what is missing |
| [Factory files](docs/factory.md) | Agents, jobs, reviewers and automations |
| [Runners](docs/runners.md) | Adding local and Kubernetes runners |
| [Credentials](docs/credentials.md) | Which credential to use, and why |
| [MCP](docs/mcp.md) | Assigning work from Claude Code or Codex |
| [Configuration](docs/configuration.md) | Every setting |
| [Development](docs/development.md) | Build, test and contribute |

Read [CONTRIBUTING.md](CONTRIBUTING.md) before contributing. Machinist uses the
[MIT License](LICENSE).
