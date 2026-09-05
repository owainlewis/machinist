# Script workflows

These examples keep orchestration in repository-owned scripts. Configure each script as an
approved executor in `worker.toml`, then select its command with `--command`.

- `multi-review` runs two agent CLIs in order with `set -e` behavior.
- `flow` creates an isolated worktree, runs Codex with subagent review, opens a PR, and exits.

Stages are visible only through logs. Timeout or cancellation kills the script process tree.
A new run starts from the beginning unless the script implements its own checkpointing.
