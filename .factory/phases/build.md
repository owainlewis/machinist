---
name: build
description: Implement one scoped issue and open a pull request
runtime: claude
timeout: 2h
---
Implement {{ issue.identifier }}.

{{ issue.title }}

{{ issue.body }}

Acceptance criteria:
{{ run.criteria }}

If PLAN.md is present in the repository root, follow it and delete it before
you commit.

You are on a clean worktree of {{ run.repo }}. Make the change, run the tests,
and commit. Open a pull request when the tests pass.

Where the issue is ambiguous, take the most reasonable reading, record the
assumption at the top of the pull request description, and continue. Do not
stop to ask a question: a run that waits for an answer has failed to be
useful. Match the conventions already in the surrounding code.
