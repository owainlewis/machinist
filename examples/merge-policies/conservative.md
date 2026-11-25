# Conservative Merge Policy (Tests & Docs Only)

Code quality has already been checked by the review step, and CI runs before merge.
This policy is for teams starting out with auto-merge or operating in higher-risk codebases.
When in doubt, always choose `human_review`.

Auto-merge (`decision: "approve"`, `risk: "low"`) **only** when the entire diff is:
- Unit or integration tests (adding or fixing tests without weakening assertions)
- Documentation, READMEs, comments, or copy-only text tweaks
- Small, tightly scoped diffs (under 300 changed lines) touching zero production code

Require human review (`decision: "human_review"`, `risk: "medium"` or `"high"`) for:
- Any change to production application code, backend logic, or frontend behavior
- Database migrations, schema changes, or storage operations
- Authentication, authorization, secrets, or security headers
- CI/CD workflows, Dockerfiles, deployment configs, or dependency changes
- Deleting, skipping, or weakening existing tests
