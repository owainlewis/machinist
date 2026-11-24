# Default Merge Policy

Auto-merge ONLY when the change is clearly low risk:
- Documentation updates, typos, comments, or copy tweaks
- Unit tests or test fixtures without risky production changes
- Small, localized bug fixes or internal refactors with clear test coverage

Require human review (`decision: "human_review"`) for medium- or high-risk changes, including:
- Database schema changes, SQL migrations, or destructive data operations
- Authentication, authorization, permissions, cryptography, or security boundaries
- Billing, payments, or breaking external API contract changes
- CI/CD workflows, deployment configs, infrastructure, or dependency upgrades
- Broad cross-cutting changes or anything where safety is uncertain
