# Factory

**Run repeatable software work through AI coding agents across repositories and machines.**

[![CI](https://github.com/owainlewis/factory/actions/workflows/ci.yml/badge.svg)](https://github.com/owainlewis/factory/actions/workflows/ci.yml)
[![MIT license](https://img.shields.io/badge/license-MIT-white.svg)](LICENSE)
[![Developer preview](https://img.shields.io/badge/status-developer%20preview-5b7cfa.svg)](#project-status)

[Website](https://owainlewis.github.io/factory/) ·
[Quick start](#quick-start) ·
[Documentation](docs/README.md) ·
[Architecture](ARCHITECTURE.md) ·
[Contributing](CONTRIBUTING.md)

Factory is an open-source, local-first control plane for coding agents. Define
software work once, run it across one or many Git repositories, and see every
agent, worktree, result, failure, and retry in one place.

It is designed for builders who have outgrown a collection of terminal windows
but still want agents to run on infrastructure and credentials they control.

```text
Define work  ->  Dispatch repositories  ->  Run agents  ->  Inspect outcomes
                       |                       |
                       +-- Git worktrees       +-- Pi, Codex, Claude Code
                       +-- local or VM Workers +-- bounded concurrency
```

## What Factory gives you

- **Repeatable software work.** Save a prompt, repository scope, runtime,
  schedule, and execution settings as one Task.
- **One operational view.** Follow active Runs, repository Sessions, Attempts,
  agent events, results, failures, and retained worktrees from the browser.
- **A worker fleet you control.** Run Pi, Codex, or Claude Code on a laptop,
  workstation, or remote VM without exposing the operator API publicly.
- **Git-native isolation.** Every Attempt runs in its own worktree. Clean work
  is reclaimed while unpublished or failed work remains inspectable.
- **Durable coordination.** SQLite state, leases, heartbeats, retries,
  cancellation, schedules, and bounded APIs survive process restarts.

## Quick start

Requirements:

- Go 1.25.13 or newer on the 1.25 release line, or Go 1.26.6 or newer
- Git, `curl`, and `just`
- An authenticated Pi, Codex, or Claude Code CLI on the Worker host
- GitHub CLI when using managed GitHub repositories

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
just build
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
just run
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337). Edit
`~/.factory/worker.toml` to enable the agent runtimes installed on your machine.
Use `~/.factory/bin/factory status` to read current Runs or
`~/.factory/bin/factory workers` to check the Worker pool from the terminal.
Use `~/.factory/bin/factory build ISSUE...` to admit up to 100 GitHub issues or
repository-scoped opaque references as independent Work in one Run.
The [local guide](docs/local.md) covers authentication, repository setup, and
the complete first Run.

Node.js is only required when changing the browser UI. Normal builds use the
committed embedded assets.

## How it works

The Go control plane owns Tasks, Runs, schedules, durable state, and admission.
Workers poll for eligible Sessions, prepare isolated Git worktrees, supervise
the selected coding-agent runtime, and report bounded events and results.

```text
Browser
   |
   | loopback HTTP + JSON
   v
Factory control plane
  SQLite, scheduler, Run admission, embedded UI
   ^
   | authenticated polling, leases, events, completion
   |
Factory Workers
  repository cache, isolated worktrees, agent slots
   |
   +-- Pi
   +-- Codex
   `-- Claude Code
```

The operator surface stays on loopback. Remote Workers use a separate,
TLS-authenticated endpoint. The control plane stores coordination metadata,
while Workers retain Git contents, runtime credentials, and worktrees. Read the
[architecture](ARCHITECTURE.md) and [security policy](SECURITY.md) before
running untrusted code.

## Project status

Factory is in **developer preview**. Compatibility-breaking changes are
expected while the product model settles.

Implemented today:

- Go control-plane API and embedded React UI
- durable Tasks, Runs, Sessions, Attempts, leases, events, and cancellation
- transactional multi-item `factory build` admission with durable replay keys
- manual and scheduled work across one or many repositories
- Pi, Codex, and Claude Code Worker capabilities
- managed repository catalog, Worker caches, and isolated Git worktrees
- table, list, and Kanban Run views with repository-level detail
- local Workers and authenticated remote VM Workers

Active product work is moving Factory toward an agent-directed queue for work
items and repository fleets. Existing coding agents own engineering judgment;
Factory owns repeatable procedures, Worker capacity, durable state, and one
view of the work. See the
[target design](docs/software-factory/design.md) and
[product vision](docs/software-factory/vision.md).

## Development

```sh
just test
just vet
just ui-check
```

Browser-facing changes are proven against a real Go server with:

```sh
just test-browser
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full check set and project
workflow. Proposed and historical designs are indexed in [docs](docs/README.md).

## License

Factory is available under the [MIT License](LICENSE).
