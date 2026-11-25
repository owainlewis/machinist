# Strict Compliance Merge Policy (Fintech / Mission-Critical)

Code quality has already been checked by the review step, and CI runs before merge.
Default to `human_review`. Only clearly zero-blast-radius changes may auto-merge.

Auto-merge (`decision: "approve"`, `risk: "low"`) **only**:
- Documentation, runbooks, and inline comments
- Additive unit tests that do not modify production source files or test helpers
- Diffs under 200 changed lines with zero config, schema, or dependency changes

Require human review (`decision: "human_review"`, `risk: "high"`) for:
- All production runtime code changes
- Any database migration, query change, ledger, or storage operation
- Any authentication, authorization, cryptography, PII handling, or audit logging change
- Any billing, payment, settlement, or external API contract change
- Any CI/CD, container, infrastructure, or third-party dependency change (including patch bumps)
- Any diff over 200 lines or any deletion/weakening of tests or safety checks
