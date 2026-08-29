# Code audit

This example inspects a repository for correctness bugs. It separates discovery from
verification and opens at most three deduplicated GitHub issues. It never changes code.

Machinist does not schedule jobs yet, so this is a manual run. The prompt is ready to use
unchanged if scheduled intake is added later.

## Set up

You need authenticated `git`, `gh`, and `codex` commands. Initialize Machinist once and make
sure the `codex` executor exists in `~/.machinist/worker.toml`:

```sh
machinist init
```

Set these paths for your checkouts:

```sh
MACHINIST_EXAMPLE_ROOT=/absolute/path/to/machinist/examples/workflows/code-audit
MACHINIST_TARGET_REPO=/absolute/path/to/the/repository
```

## Run it

Name a bounded area rather than asking for a vague review of everything:

```sh
machinist run \
  --machinist-config="$MACHINIST_EXAMPLE_ROOT/config.toml" \
  --command=code-audit \
  --repo="$MACHINIST_TARGET_REPO" \
  --prompt="Audit request validation and SQLite persistence for correctness bugs"
```

The final line reports the number of candidates, independently verified bugs, and issue
URLs. No issue means the run found no bug that met the evidence bar.
