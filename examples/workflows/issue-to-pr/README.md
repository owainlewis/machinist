# Issue to pull request

This example gives one Codex foreman a GitHub issue. The foreman decides whether the issue
needs refinement, then delegates implementation and review to fresh native subagents. It
opens a pull request but never merges it.

## Set up

You need authenticated `git`, `gh`, and `codex` commands. Initialize Machinist once and make
sure the `codex` executor exists in `~/.machinist/worker.toml`:

```sh
machinist init
```

Set these paths for your checkouts:

```sh
MACHINIST_EXAMPLE_ROOT=/absolute/path/to/machinist/examples/workflows/issue-to-pr
MACHINIST_TARGET_REPO=/absolute/path/to/the/repository
```

## Run it

Pass one open issue URL in the prompt:

```sh
machinist run \
  --machinist-config="$MACHINIST_EXAMPLE_ROOT/config.toml" \
  --command=issue-to-pr \
  --repo="$MACHINIST_TARGET_REPO" \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```

The run succeeds when the issue is ready for human review. It may instead stop with
`machinist:needs-human` for a missing decision or `machinist:blocked` with concrete evidence.

## Use a different Codex model

If your worker maps the alias, add it to the same command:

```sh
--model=terra
```
