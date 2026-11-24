# Default Merge Policy

Code quality has already been checked by the review step, and CI checks run
before merge. Judge **blast radius and reversibility**.

Auto-merge (`decision: "approve"`, `risk: "low"`) normal, reversible engineering work:
- Features, UI updates, bug fixes, refactors, additive security headers/checks, and internal improvements with appropriate tests
- Unit or integration tests, documentation, copy tweaks, and routine maintenance
- Well-scoped changes matching the request that can be safely undone with a single `git revert`

Require human review (`decision: "human_review"`, `risk: "medium"` or `"high"`) for
high-blast-radius or hard-to-revert changes:
- **Data & schema:** database migrations, schema changes, or destructive data/storage operations
- **Security & identity:** authentication, authorization, permissions, session/cookie/token logic, cryptography, or secrets
- **Money & external contracts:** billing, payments, pricing, or breaking public API / wire-protocol changes
- **Release & supply chain:** CI/CD workflows, deployment/infrastructure configs, or new/major dependency upgrades
- **Integrity red flags:** disabling or deleting tests/safety checks, or sweeping unrelated changes outside the requested task
