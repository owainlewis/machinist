# Fast Startup Merge Policy (Solo & Small Teams)

Code quality has already been checked by the review step, and CI runs before merge.
Optimize for shipping small, reversible batches quickly; stop only for changes that are
hard to undo or carry high security or financial blast radius.

Auto-merge (`decision: "approve"`, `risk: "low"`) reversible engineering work:
- Product features, UI changes, bug fixes, refactors, and performance improvements with tests
- Tests, docs, copy, developer scripts, and patch/minor dependency updates
- Additive, backwards-compatible API endpoints or non-destructive config defaults
- Anything a single `git revert` undoes safely in production

Require human review (`decision: "human_review"`, `risk: "medium"` or `"high"`) for:
- **Irreversible data changes:** dropping columns/tables, destructive migrations, or backfill scripts
- **Auth & secrets:** login, sessions, permissions, multi-tenant isolation, tokens, or secrets
- **Billing:** payments, subscriptions, or pricing changes
- **Deploy & CI:** workflow files, production infrastructure, or major dependency upgrades
- **Integrity red flags:** skipping/deleting tests or changing code well outside the task
