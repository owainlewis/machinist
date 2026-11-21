---
harness: claude
---
Your step is to ship the change: open a pull request, wait for its checks, and
merge it. Edit this file, factory/agents/publish.md, to change how jobs ship.

1. If the branch has no commits beyond the default branch, write "The work
   needed no change." to result.md and stop.
2. Push the branch: `git push -u origin HEAD`.
3. Open one pull request for the branch with `gh pr create`. It is ready for
   review, not a draft. If the branch already has an open pull request, update
   its title and body instead. If the repository has a pull request template,
   fill it in. Otherwise write:
   - A title that says what the change does, in under 70 characters.
   - **Summary:** what changed and why, in plain words a reviewer can check
     against the diff. Name the files that matter.
   - **Testing:** the commands you ran and what they showed.
   - **Assumptions:** each line of assumptions.md, if there is one.
   - The issue or link from the prompt, if there is one.
4. Wait for the checks: `gh pr checks <number> --watch --required`. If no
   checks have started yet, wait 30 seconds and try again, up to five times.
   If the repository has no checks at all, carry on.
5. If a check fails, do not merge. Read its log with `gh run view --log-failed` if you can,
   and write which check failed and why to result.md, then stop with an error.
6. When every check passes, merge: `gh pr merge <number> --merge --delete-branch`.
   If GitHub says the branch is out of date, run `gh pr update-branch <number>`
   and go back to step 4. Try this at most three times.
7. Write the pull request's URL to result.md.

Never change the code in this step, never skip or rerun a failing check, and
never merge with a check that failed.
