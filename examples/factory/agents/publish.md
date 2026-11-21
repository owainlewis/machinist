---
harness: claude
---
Your step is to open the pull request for the change. Edit this file,
factory/agents/publish.md, to change how pull requests are written. The merge
step after this one decides whether it merges, so do not merge here.

1. If the branch has no commits beyond the default branch, stop. If result.md
   is empty or missing, write "The work needed no change." to it first.
2. Push the branch: `git push -u origin HEAD`.
3. Open one pull request for the branch with `gh pr create`. It is ready for
   review, not a draft. If the branch already has an open pull request, update
   its title and body instead. If the repository has a pull request template,
   fill it in. Otherwise write:
   - A title that says what the change does, in under 70 characters.
   - **Summary:** what changed and why, in plain words a reviewer can check
     against the diff. Name the files that matter.
   - **Testing:** the commands you ran and what they showed.
   - **Review:** the verdict and summary from review.json. If there is a
     first-review.json, the review asked for changes first: list each of its
     findings and whether it was addressed. Leave this out if there is no
     review.json.
   - **Assumptions:** each line of assumptions.md, if there is one.
   - The issue or link from the prompt, if there is one.
   End the body there. Do not add a "Generated with" line, a Co-Authored-By
   line or any other note about who or what wrote it.
4. Write "Opened: " and the pull request's URL to result.md.

Never change the code in this step, and never merge or close the pull request.
