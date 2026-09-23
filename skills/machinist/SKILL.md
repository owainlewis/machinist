---
name: machinist
description: Use Machinist to create, assign, and monitor software tasks. Use when a coding agent needs to work with Machinist, its GitHub issue workflow, direct runs, or managed queue.
---

# Machinist

Machinist turns a task or GitHub issue into an implemented, independently reviewed, and
checked pull request. It never merges the pull request.

## Core model

- A task is one GitHub issue in the target repository, or a plain description.
- Assigning a task means starting `machinist run` or queuing `machinist submit`.
- Assign the same issue again to continue interrupted work. The `task-to-pr` command
  reuses an existing branch, worktree, and pull request for the task.
- When `MACHINIST_RUN_ID` is set, the agent is already inside a Machinist run. Follow the
  assigned task and do not start or submit another run.

## Create a task

Reuse a supplied issue when it is open and belongs to the current repository. Otherwise
create one issue with `gh issue create`. Keep it focused on one observable outcome and
preserve the user's constraints. Do not invent implementation details that the request
does not decide.

```sh
gh issue create --title "<short outcome>" --body "<problem, outcome, constraints, and acceptance evidence>"
```

Use the issue URL returned by GitHub for every later command.

## Assign a task

Use direct mode for immediate local work. Pass an absolute Git worktree path:

```sh
machinist run \
  --command=task-to-pr \
  --repo=/absolute/path/to/repository \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```

Use managed mode when the control plane and worker are already running. Pass the logical
repository name from `worker.toml`:

```sh
machinist submit \
  --command=task-to-pr \
  --repo=repository-name \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```

`submit` prints a job ID. Follow managed work in the local control-plane UI.

Machinist does not watch GitHub issues or labels. Submit work directly.

## Report status

Check the run in the control-plane UI or the `machinist run` output, then the linked pull
request and its checks. The command finishes as completed, blocked, or failed with a short
summary.

- For blocked work, fix the reported cause or answer the question, then assign the same
  issue again.
- For completed work, hand the pull request to a person. Never merge unless that person
  explicitly decides to do so.

When reporting status, include the issue URL, job ID when managed, pull request URL when
created, checks, and blocker or next human action.
