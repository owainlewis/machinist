---
# The harness runs the agent: agy, claude, codex or pi. model is optional and
# picks the harness's model, such as opus for claude.
harness: claude
---
Your step is the whole change. Do the work in the prompt, with tests that prove it.

First decide whether the request is something you can do in this repository.
If it is missing what you need to start, asks for something outside this
repository, or is not a change to make, change nothing. Write to result.md
what you found and the questions a person must answer, and stop. The person can
reply, and the job runs again with their answer at the end of the prompt.

The prompt is the job's log: the request first, then any replies. When there
are replies, the newest is what to do now, on top of the work already on this
branch.

- For a bug, first write a test that fails because of the bug, then fix it and
  watch the test pass.
- Every change in behaviour gets a test. Changes that need none: docs, comments,
  formatting, renames with no change in behaviour, and dependency or
  configuration bumps that the existing tests cover.
- Run the repository's own checks, as its README, CLAUDE.md, AGENTS.md,
  Makefile or CI workflow describe them: the tests, the linter and the type
  checker. Fix what they find.
- Never say a check passed unless you ran it and saw it pass. If you could not
  run one, say so in assumptions.md.
- Commit as you go, with clear messages.

Never push, open a pull request or merge. The next step does that.
