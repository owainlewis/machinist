---
name: machinist
description: "Routes state-changing coding work from an interactive Codex chat through Machinist. Use by default whenever the user asks to implement, build, fix, refactor, migrate, add, remove, or otherwise change code, even when another coding workflow skill also matches. Do not use for read-only discussion, planning, explanation, or review, or when MACHINIST_RUN_ID is set."
user-invocable: true
argument-hint: "<coding task or one GitHub issue URL>"
---

# Machinist

Route coding changes through the Machinist foreman. The current Codex chat is the
dispatcher, not the implementer.

## Boundary

Use this skill for any request whose intended result changes repository files. This
includes implementation, bug fixes, refactors, migrations, dependency changes, and
follow-up fixes from an issue or pull request.

Do not use it for read-only questions, investigation, design, planning, explanation, or
review. If the user explicitly asks to work in the current chat instead of Machinist,
follow that instruction.

Never delegate when `MACHINIST_RUN_ID` is non-empty. That variable means Machinist already
started the current executor. Follow the supplied Machinist role and workflow instead.

## Hard rules

- Do not edit code, create a task branch, run implementation checks, push, or open a pull
  request in the dispatching chat.
- Do not fall back to implementing in the chat when Machinist, GitHub, or its executor is
  unavailable. Report the exact setup failure and stop.
- Do not invoke a competing implementation skill such as `task-to-pr`. Machinist owns the
  full build, test, independent review, repair, CI, and pull-request workflow.
- Do not merge. The Machinist foreman hands an open, verified pull request to the user.

## Dispatch

1. Check `MACHINIST_RUN_ID` before doing anything else. If it is set, stop this skill and
   continue the existing Machinist role.
2. Resolve the current Git repository root and its GitHub remote. Stop if the target is
   not an unambiguous GitHub repository.
3. Resolve one issue:
   - If the user supplied exactly one GitHub issue URL, verify that it belongs to the
     current repository and is open. Reuse it.
   - Otherwise create one open GitHub issue from the user's request. Preserve the requested
     outcome, constraints, and relevant evidence. Keep the issue factual and do not invent
     a solution. Issue creation is intake, not implementation.
   - If the request contains clearly independent changes, create one issue and one run per
     change. Run overlapping work sequentially.
4. Tell the user which issue is entering Machinist.
5. From this skill directory, run:

   ```sh
   ./scripts/delegate.sh "/absolute/repository/root" "https://github.com/owner/repo/issues/123"
   ```

   Let the command stream. During a long run, relay only concise phase changes or blockers.
   Do not duplicate the foreman's implementation work in the chat.
6. Wait for the command to finish. Report the issue, pull request when created, final
   label, checks, independent review verdict, and repair count from the foreman's result.

## Existing pull requests and resumed work

Still dispatch the issue when work already exists. The foreman discovers and resumes its
recorded branch, worktree, pull request, review state, and CI state. Do not create a second
branch or pull request from the chat.

## Failure handling

A helper exit before the executor starts is a dispatch failure. Show its stderr and the
specific action needed, such as installing `machinist`, running `machinist init`, or
authenticating `gh` or Codex. Once the foreman starts, treat its final
`machinist:needs-human` or `machinist:blocked` state as authoritative and return the stated
question or evidence.
