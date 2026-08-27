---
name: machinist
description: Use Machinist to create, assign, monitor, and resume software tasks. Use when a coding agent needs to work with Machinist, its GitHub issue workflow, lifecycle labels, direct runs, or managed queue.
---

# Machinist

Machinist turns one open GitHub issue into a planned, implemented, independently
reviewed, and checked pull request. It never merges the pull request.

## Core model

- A task is one open GitHub issue in the target repository.
- Assigning a task means starting `machinist run` or queuing `machinist submit`.
- Labels report workflow state. They do not assign or start work.
- Reassign the same issue to resume existing work. The foreman recovers its branch,
  worktree, pull request, checks, and repair count.

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
  --agent=foreman \
  --repo=/absolute/path/to/repository \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```

Use managed mode when the control plane and worker are already running. Pass the logical
repository name from `worker.toml`:

```sh
machinist submit \
  --agent=foreman \
  --repo=repository-name \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```

`submit` prints a job ID. Follow managed work in the local control-plane UI. Do not treat
adding a label as submission. Label-based delegation is not implemented.

## Lifecycle labels

Keep exactly one lifecycle or exception label on the issue:

| Label | Meaning | Agent action |
| --- | --- | --- |
| `machinist:planning` | The issue is being refined into a clear task. | Wait for planning or answer a question if asked. |
| `machinist:building` | A worker is implementing or repairing the change. | Do not start overlapping work. |
| `machinist:verifying` | Independent review, CI, or automated review is running. | Wait for the current head to finish verification. |
| `machinist:ready-for-review` | The pull request is verified and ready for a person. | Review and merge manually when satisfied. |
| `machinist:needs-human` | A product or technical decision is missing. | Answer the precise issue question, then assign the same issue again. |
| `machinist:blocked` | Tooling, credentials, infrastructure, or the repair limit stopped work. | Read the evidence, remove the external blocker, then assign the same issue again. |

The foreman creates and transitions these labels. If a label is missing during manual
recovery, create it with GitHub CLI, then add it to the issue:

```sh
gh label create "machinist:planning" --color 1d76db --description "Machinist is planning this task"
gh issue edit https://github.com/owner/repository/issues/123 --add-label "machinist:planning"
```

Use the same pattern for the label named in the table. Remove the previous lifecycle label
before adding another. Do not change a label merely to make progress appear further along.

## Manage and resume work

Inspect the issue labels, the `<!-- machinist:foreman-state -->` issue comment, the linked
pull request, and current checks. Treat that recorded state as the source of truth.

- For `machinist:needs-human`, answer the issue question and reassign the same issue.
- For `machinist:blocked`, fix the reported external cause and reassign the same issue.
- For interrupted or stale work, reassign the same issue. Do not create a second branch or
  pull request.
- For `machinist:ready-for-review`, hand the pull request to a person. Never merge unless
  that person explicitly decides to do so outside the shipped foreman workflow.

When reporting status, include the issue URL, job ID when managed, pull request URL when
created, current label, checks, review verdict, and blocker or next human action.
