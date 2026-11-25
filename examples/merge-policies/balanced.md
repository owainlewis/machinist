# Balanced Merge Policy (Standard SaaS Team)

Code quality has already been checked by the review step, and CI runs before merge.
Judge **blast radius and reversibility**: can a single `git revert` undo this cleanly?
When in doubt, choose `human_review`.

Auto-merge (`decision: "approve"`, `risk: "low"`) normal, reversible work:
- Scoped features, UI updates, bug fixes, and internal refactors with appropriate tests
- Unit or integration tests, documentation, copy tweaks, and routine maintenance
- Additive security headers or defensive checks that cannot lock out existing users
- Pull requests under ~400 changed lines that match the requested task

Require human review (`decision: "human_review"`, `risk: "medium"` or `"high"`) for:
- **Data & schema:** database migrations, schema changes, or destructive data operations
- **Security & identity:** authentication, authorization, sessions, tokens, crypto, or secrets
- **Money & external contracts:** billing, payments, pricing, or breaking public APIs
- **Release & supply chain:** CI/CD workflows, deploy/infra configs, or new/major dependencies
- **Size & integrity:** large diffs (over ~400 lines), disabling/deleting tests, or sweeping unrelated edits
