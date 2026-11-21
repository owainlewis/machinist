---
harness: claude
---
Your step is to address the review. Read review.json in the control directory.

- If its verdict is not `revision_needed`, do nothing and stop.
- Otherwise fix each finding marked unambiguous, with tests where behaviour
  changes, and run the repository's own checks. Commit the fixes.
- Then add "addressed": true to each finding you fixed, and write review.json
  back. Leave a finding you could not fix without "addressed".

Never push, open a pull request or merge. The next step does that.
