# Getting started

This guide takes Machinist from a source checkout to its first supervised coding
run.

## Requirements

Machinist runs on macOS and Linux. You need:

- Go 1.26.6 or newer
- Git
- the executable used by your agent definition
- an authenticated GitHub CLI for the shipped `foreman` and `audit` agents

The default configuration includes Codex and Claude executors. The shipped
agents use Codex, so the shortest path also requires an authenticated Codex CLI
with native subagents available.

Node.js and `just` are only needed when you change and rebuild the control-plane
frontend.

## Build Machinist

```sh
mkdir -p ./bin
go build -o ./bin/machinist ./cmd/machinist
```

Confirm the binary works:

```sh
./bin/machinist version
./bin/machinist --help
```

Source builds report the version as `dev` unless a release build supplies a
version at link time.

## Create the default configuration

```sh
./bin/machinist init
```

Machinist creates these files:

```text
~/.machinist/
├── config.toml
├── worker.toml
├── agents/
│   ├── audit.md
│   ├── foreman.md
│   └── shepherd.md
└── server/
    └── worker.token
```

All files are created with user-only permissions. Re-running `machinist init`
creates missing files and keeps existing files unchanged.

Review the prompts before running them. The default agents are trusted local
automation and the Codex executor uses `danger-full-access` so it can use GitHub
and create worktrees outside the target repository.

## Run the foreman

Choose an open issue in the target GitHub repository. A narrow issue with an
observable outcome makes the first run easy to judge.

```sh
./bin/machinist run \
  --agent=foreman \
  --repo=/absolute/path/to/your-repository \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123"
```

The default foreman:

1. turns the issue into a small specification;
2. gives implementation to a fresh coding agent;
3. gives the finished change to a different review agent;
4. sends valid findings through at most two repair attempts;
5. opens a non-draft pull request and waits for available checks; and
6. hands the verified pull request back without merging it.

It also maintains `machinist:*` lifecycle labels on the issue so progress is
visible where the work started.

## Run an audit

The audit agent keeps the repository read-only. It delegates inspection,
requires independent verification for every candidate bug, checks open issues
for duplicates, and may open up to three issues for bugs it can prove.

```sh
./bin/machinist run \
  --agent=audit \
  --repo=/absolute/path/to/your-repository \
  --prompt="Audit the request handling and persistence code"
```

Creating no issue is a valid result when nothing meets the evidence bar.

## Opt in to scheduled merges

Foreman still stops at a verified pull request. To run a separate merge queue, create the
`machinist:auto-merge` label, add a `[shepherd.<name>]` schedule to `config.toml`, and run
the control plane and managed worker. Apply the label only to pull requests Shepherd may
change. See [Configuration](configuration.md#schedule-shepherd) for the schedule and label
commands.

## Choose a model

The default worker configuration maps short aliases to models. Select one for a
run with `--model`:

```sh
./bin/machinist run \
  --agent=foreman \
  --model=terra \
  --repo=/absolute/path/to/your-repository \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123"
```

Omit `--model` to use the executor's normal default.

## Next steps

- Read [How Machinist works](how-it-works.md) for execution modes and run
  artifacts.
- Read [Configuration](configuration.md) to change prompts, executors, models,
  or pipelines.
- Set up the [local control plane](control-plane.md) when you want a browser UI
  and managed queue.
