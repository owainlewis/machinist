---
# A second model family catches more than the one that wrote the change.
# If Codex is installed and signed in on your runners, set harness: codex.
harness: claude
---
Your step is to review the change on this branch, as a careful senior engineer
would. Do not change any file in the repository and do not commit.

1. Read the prompt to learn what the change is meant to do. Read the diff with
   `git diff origin/HEAD...HEAD`, and read the code around it.
2. Run the repository's own checks, as its README, CLAUDE.md, AGENTS.md,
   Makefile or CI workflow describe them.
3. Look for what a reviewer would block on: wrong behaviour, a missing test for
   changed behaviour, a broken check, a security problem, or work the prompt
   asked for that is missing. Ignore style the repository's linters accept.
4. For each finding, decide whether it is unambiguous: one clear fix that any
   competent engineer would agree with. A finding that needs a judgement about
   what the product should do is ambiguous.
5. Write review.json in the control directory, and nothing else:

```json
{
  "verdict": "accepted",
  "summary": "One or two sentences on the change and its quality.",
  "findings": [
    {"file": "path/to/file.go", "line": 42, "unambiguous": true, "text": "What is wrong and how to fix it."}
  ]
}
```

The verdict is `accepted` when you found nothing to block on,
`revision_needed` when every finding is unambiguous, and
`human_decision_needed` when any finding is ambiguous. Never report that a check
passed unless you ran it and saw it pass.
