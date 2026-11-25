# Merge Policy

Code quality has already been checked by the review step, and CI checks run before merge.
Judge strictly on **blast radius and reversibility**: avoid review theater — if a change is scoped to the requested task, passes tests, and can be undone cleanly with a single `git revert`, approve it.

Auto-merge (`decision: "approve"`, `risk: "low"`) normal, reversible engineering work:
- Features, UI updates, bug fixes, internal refactors, and performance improvements with appropriate tests — regardless of line count when the diff is coherent and scoped to the task
- New HTTP API, MCP, CLI, webhook, runner backend, or observability endpoints and handlers
- Additive, backwards-compatible database schema changes (new tables or nullable/defaulted `ADD COLUMN` migrations) accompanied by migration tests
- Unit, integration, and end-to-end tests, documentation, copy tweaks, and development configuration
- Patch or minor dependency updates and additive security headers or defensive checks that cannot lock out existing users

Require human review (`decision: "human_review"`, `risk: "medium"` or `"high"`) only for genuinely high-blast-radius or hard-to-reverse changes:
- **Destructive data & schema:** dropping tables or columns, altering existing column types destructively, or rewriting/deleting production data
- **Core auth & multi-tenant security boundaries:** login/OAuth flows, session/cookie handling, token-scope enforcement, tenant isolation, cryptography, or secrets management
- **Billing & breaking contracts:** payments, pricing, billing logic, or breaking changes to public/wire APIs
- **CI/CD & production infrastructure:** changes to `.github/workflows/`, production deployment/infrastructure configs, or major dependency upgrades
- **Integrity:** disabling, weakening, or deleting tests to make CI pass, or sweeping changes unrelated to the requested task
