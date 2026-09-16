# Development

## Requirements

- Go 1.26.6 or newer
- Node.js and npm for the control-plane frontend
- `just` to use the repository shortcuts

## Build

Build the React application, embed it, and compile Machinist:

```sh
just build
```

The binary is written to `bin/machinist`.

For backend-only changes that do not touch the frontend, the tracked production
assets allow a direct Go build:

```sh
go build ./...
```

## Run locally

Start the control plane and managed worker together:

```sh
just local
```

The control plane uses the repository's `examples/config.toml`. The managed
worker continues to use `~/.machinist/worker.toml` because executors,
credentials, and repository paths are machine-owned configuration.

## Issue launcher

Run the local workflow from the target repository with an authenticated `gh` and
`codex` on `PATH`:

```sh
uv run agent.py https://github.com/OWNER/REPO/issues/123
```

Python verifies the issue repository against `origin` before starting Codex. The
current check supports standard github.com SSH and HTTPS remotes; custom SSH host
aliases need a canonical origin URL. The agent implements the task and opens a PR,
then Python waits for checks and known active reviews and allows up to three repair
passes.

Feedback includes PR comments, unresolved review threads, and failed GitHub Actions
step logs. Outdated unresolved findings stay available for assessment. Log excerpts
are limited to 12,000 characters per job and 48,000 overall, within the remaining CI
wait budget. Missing or truncated logs keep the job link so the agent can investigate.
Raw job logs are passed to the agent and are not printed by the collector. These
excerpts are not a general secret-redaction mechanism.

## Verify

Run the complete project check before opening a pull request:

```sh
just check
```

This installs the locked frontend dependencies, runs frontend tests, rebuilds
the embedded assets, runs Python eval tests, checks and vets the Go code, runs
Go tests with the race detector, and builds all Go packages.

Focused commands are also available:

```sh
just test
python3 -m unittest discover -s evals -p 'test_*.py'
cd internal/controlplane/web && npm test
go test ./internal/runner
```

## Project layout

```text
cmd/machinist/                CLI entry point
examples/                     embedded default configuration and prompts
internal/cli/                 command behavior
internal/config/              strict TOML loading and template resolution
internal/runner/              process execution and event recording
internal/controlplane/        HTTP server, SQLite store, and embedded UI
internal/managedworker/       polling, leases, execution, and result delivery
docs/                         user and design documentation
```

The frontend source lives in `internal/controlplane/web/src`. Its production
bundle lives in `internal/controlplane/web/dist` because Go embeds those files at
compile time.
