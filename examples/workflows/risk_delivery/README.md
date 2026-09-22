# Classify then merge

Build PR → independent risk review → deterministic policy → merge.

Add config.toml's definitions to the server config and copy the four prompt files
beside that config. Register these worker executors with an absolute path to a
trusted copy of gate.py outside repositories agents can edit:

```toml
[executors.risk-gate]
command = ["python3", "/trusted/risk-delivery/gate.py", "gate", "owainlewis/machinist"]
[executors.risk-merge]
command = ["python3", "/trusted/risk-delivery/gate.py", "merge", "owainlewis/machinist"]
```

Change the repository allowlist argument for your repository. Requires Python 3,
gh authentication, repository write access, and an existing codex executor.
Submitting a task to this pipeline authorizes pushing a new branch, opening a PR,
and merging changes that satisfy the policy. No existing PR is selected for you.

The automatic path is deliberately narrow: low-rated changes to at most ten
Markdown/text documentation files, at most 200 added/deleted lines, no renames,
symlinks or executable files. All other changes require human approval. A human
approval does not bypass CI, review-thread, mergeability or exact-head checks.
Missing checks block merging. Unknown mergeability blocks; retry after GitHub
settles. The merge command uses squash and --match-head-commit, never --admin or
--auto. Repository branch protection remains authoritative. Protect required CI
and review rules on GitHub; a local preflight cannot atomically freeze all remote
review/check state alongside a merge.

Stages share `{{task.output_dir}}`; no input mappings are needed. Each stage
receives an immutable snapshot and publishes its updated files. Changed heads
block and require a new build/review. A changed base also requires policy review
again. Retrying an already-confirmed merge is idempotent. Approval-required results are persisted into the workflow plan so
restarts and retries retain the gate. The policy step is separate from the agent
review; Request changes at that gate reruns policy, not the build. To change code,
start a fresh task with the requested correction. This is a v1 limitation.

The existing danger-full-access agent executor is not a security sandbox. The
trusted script enforces workflow behavior, but truly isolating agent credentials
and its ability to invoke GitHub directly requires separate restricted workers.

Test without contacting GitHub:
`python3 -m unittest discover -s examples/workflows/risk_delivery -p 'test_*.py'`
