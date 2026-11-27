# Merge policies

A merge policy tells Machinist's `merge-gate` step which pull requests are safe to auto-merge
once CI and AI review pass, and which ones must wait for a person.

The policy is a plain Markdown file committed to a repository's default branch at
`.machinist/merge-policy.md`. The judge reads it via
`git show origin/HEAD:.machinist/merge-policy.md` — never from the pull request branch — so a
pull request cannot relax its own rules. If a repository has no `.machinist/merge-policy.md`,
Machinist falls back to `factory/prompts/merge-policy.md`.

## How to choose a policy

Pick the policy that fits your repository. Start from one question:

> *"What risks are our pull requests actually managing here — and what can a single `git revert` undo safely?"*

The right bar depends on **blast radius** and **team size**. Smaller teams with high shared
context and fast rollbacks can auto-merge more; regulated or mission-critical systems should
keep humans on all runtime code.

## Example starter policies

Pick the closest match and copy it to `.machinist/merge-policy.md` on your default branch:

| Example | Best for | Auto-merges | Always requires a human |
| --- | --- | --- | --- |
| [`conservative.md`](../examples/merge-policies/conservative.md) | Week 1 rollout or cautious teams | Pure tests, docs, copy (< 300 lines) | All production code, migrations, auth, CI, deps |
| [`balanced.md`](../examples/merge-policies/balanced.md) | Standard SaaS teams | Reversible features, UI, fixes, refactors, tests, docs (< 400 lines) | Migrations, auth/secrets, billing/breaking APIs, CI/deploy/deps, large PRs, weakened tests |
| [`fast-startup.md`](../examples/merge-policies/fast-startup.md) | Solo builders & small high-context teams | Almost all 1-revert product work, UI, minor deps | Destructive migrations, auth/tenancy/secrets, billing, deploy/CI, weakened tests |
| [`strict-fintech.md`](../examples/merge-policies/strict-fintech.md) | Fintech, healthcare, mission-critical systems | Docs and additive tests only (< 200 lines) | All runtime code, queries, auth/PII/crypto, money, CI/infra, any dependency bump |

See also the default shipped policy in [`examples/factory/prompts/merge-policy.md`](../examples/factory/prompts/merge-policy.md).

## Rolling out safely (observe mode)

Start with `github.auto_merge: false` in `config.yaml`. In observe mode, Machinist still runs
`merge-gate` on every pull request and posts its decision (`approve` or `human_review`), risk
level (`low`, `medium`, or `high`), and reason as a GitHub comment — without merging anything.

Run observe mode for a week:

1. Check every PR where you disagree with the gate's verdict.
2. Tighten `.machinist/merge-policy.md` on `main` to encode those edge cases.
3. Flip `github.auto_merge: true` once the gate matches your judgment.

## Why the gate is safe

- **Default-branch policy:** Loaded from `origin/HEAD`, never the PR working tree.
- **No GitHub credentials:** `scripts/merge-gate.sh` unsets `GH_TOKEN` and `GITHUB_TOKEN` before invoking the judge agent.
- **Untrusted-input framing:** The judge prompt treats diffs, commit messages, and comments as untrusted data, never instructions.
- **Deterministic merge execution:** Plain Go code (`machinist merge --require-gate`) executes the merge only when `github.auto_merge` is `true`, `review.json` is `accepted`, `merge-gate.json` is `approve` + `low`, every CI check passed, and the PR head SHA has not changed since review.
