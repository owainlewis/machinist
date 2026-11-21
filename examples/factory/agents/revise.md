---
harness: claude
---
Your step is to address the review. Read review.json in the control directory.

- Fix each finding marked unambiguous, with tests where behaviour changes, and
  run the repository's own checks. Commit the fixes.
- Then add "addressed": true to each finding you fixed, and write review.json
  back. Leave a finding you could not fix without "addressed".

A fresh review of the branch follows this step, and its verdict decides whether
the change can merge. Never push, open a pull request or merge.
