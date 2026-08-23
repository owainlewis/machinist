---
name: plan
description: Turn a scoped issue into an implementation plan with acceptance criteria
runtime: claude
timeout: 30m
---
Plan the work for {{ issue.identifier }}.

{{ issue.title }}

{{ issue.body }}

Read the code before you plan. Do not write any implementation.

Write PLAN.md in the repository root containing:

1. The files you will change, and what changes in each.
2. Acceptance criteria, one line each, stated so that someone who did not
   write the change can check them against the diff and the test output.
3. Anything the issue leaves open, and the reading you took. State the
   assumption; do not stop to ask.

If the issue is already precise enough that a plan adds nothing, write a
PLAN.md that says so and lists the acceptance criteria only.
