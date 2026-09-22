# Workflow examples

A Machinist command runs one executable with the prompt on standard input. The same
workflow can be written in two styles.

## Prompt

The agent runs the whole flow. [`prompts/task-to-pr.md`](../prompts/task-to-pr.md) asks one
Codex session to implement a task in an isolated worktree, get an independent subagent
review, open a pull request, and repair CI and review feedback. It is the default
`task-to-pr` command:

```sh
machinist run \
  --command=task-to-pr \
  --repo=/absolute/path/to/repository \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```

## Script

Code runs the flow and calls the agent for each step. [`agent.py`](../../agent.py) takes a
GitHub issue to a pull request with short prompts and a structured result. Python checks
the pull request, waits for CI, and limits repair passes.
[`code-workflows/codex_example.py`](code-workflows/codex_example.py) is the smallest
starting point: one Codex task, read-only unless `--write` is set.

To run a script through Machinist, register it as an executor in `worker.toml` and point a
command at that executor:

```toml
# worker.toml
[executors.my-workflow]
command = ["uv", "run", "--script", "/absolute/path/to/my_workflow.py"]

# config.toml
[commands.my-workflow]
executor = "my-workflow"
timeout = "2h"
```

The script's stages appear only in its logs. Timeout or cancellation stops the whole
process tree, and a new run starts from the beginning unless the script saves its own
progress.

## Managed task workflows

For stages tracked in the UI, see [the task guide](../../docs/task-guide.md),
[staged configuration](staged/config.toml), and the optional
[classify-then-merge example](risk_delivery/README.md).

The [gVisor experiment](gvisor-pr/README.md) demonstrates an alternative isolated worker.
