Implement this issue:

{{machinist.prompt}}

Read repository instructions. Discover any existing branch and linked open PR
before making changes. Reuse matching work; stop if the relationship is ambiguous.
Work in an isolated worktree. Implement the acceptance criteria, run the relevant
checks, and obtain an independent review. Repair valid findings within a bounded
cycle. Open or update one linked PR. Never merge or force-push.

Use complete only when the change is ready for human review, with the PR URL and
verification summary in the workflow result summary. Use blocked for missing
requirements or unresolved findings, and failed for execution failures. Write the
workflow result file described by the runtime. Do not assume a prior attempt had
no effects: this step can be explicitly retried after an interruption.
