# Automations

An automation is a saved single-agent job: a name, connected repository and Markdown instructions. It uses the coding agent and runner selected in setup, including local, VM and Kubernetes runners. Each run starts in the root of its isolated repository checkout.

Open **Automations** in the sidebar, choose **New automation**, write instructions and save. **Run now** queues one normal job. Open its run to see activity, results and any PR. For a read-only review, explicitly ask the agent to report findings without changing files. Repository changes use the same PR delivery as other single-agent jobs, without a separate reviewer. Internal prompts and findings are not posted to issues.

Schedules are optional. Choose an hourly, daily or weekly preset, or a five-field cron expression with an IANA timezone such as `Europe/London`. Scheduled times use that timezone; next/last run timestamps include the viewer's timezone. Pause stops future scheduled admissions. Manual runs remain available while paused. Saving an automation never runs it immediately.

Machinist's control plane must stay running. It checks due schedules every 15 seconds. Definitions and the next due time persist in the existing local SQLite database. After downtime it queues at most one missed occurrence per automation, then advances to the next future occurrence. It does not replay a backlog.

An automation waits for all its prior jobs, including historical retries. Active work, blockers and PRs awaiting merge prevent another admission. Busy scheduled occurrences are skipped and shown on the automation. Finish the job, merge and check its status, or cancel it to release the schedule. If a cancelled attempt started a process, verify it has stopped and choose **Confirm stopped and run** before resuming. Retries of older runs cannot overlap newer work. A lost response to Run now reuses the same admission key.

Edit opens the saved instructions and schedule. Pause/Resume uses buttons. Delete requires inline confirmation and removes only the definition; existing run history remains.

## API

All mutations require the normal CSRF/origin protection or worker bearer authorization.

- `GET /api/v1/automations`: list definitions and latest run links.
- `POST /api/v1/automations`: create `{name, repository, prompt, cron, timezone, paused}`. Add
  `kind: "repository-maintenance"` for the managed maintenance type below. Empty `cron` means manual only.
- `PUT /api/v1/automations/{id}`: replace those fields with the current `version`. Stale versions return 409.
- `POST /api/v1/automations/{id}/run`: queue once with `{client_request_id}`. Reuse the key to recover a lost response. An optional `previous_process_stopped: true` explicitly confirms cancelled processes have stopped.
- `DELETE /api/v1/automations/{id}`: delete with `{version}`.

Schedule cursor advancement, job creation and the admission receipt commit in one database transaction. Jobs use the existing execution leases, cancellation, recovery, result validation and PR publisher. There is no second workflow engine or external scheduling service.

## Repository maintenance

Choose **Repository maintenance** as the automation type, select a repository, and save. Start with **Manual only** and **Run now** to inspect its first report. The same schedules work when you are ready to run it regularly.

The agent receives a read-only checkout and a snapshot of the ten most recently updated open issues and ten PRs. PR evidence includes immutable revision diffs and check results. The report says when the repository exceeds this batch. The agent classifies work, identifies missing information or likely duplicates, and suggests existing labels. The foreman adds matching non-Machinist labels through the GitHub App. The report shows confirmed label names after a successful update. Confirmed labels may already have been present. It never removes human labels or changes routing labels, closes issues or PRs, or posts internal reports and prompts to GitHub.

The foreman includes an accepted independent review head in the snapshot only
when recorded factory history binds that review to the current implementation
and PR revision. This is evidence for that SHA, not merge permission. It avoids
using stale stage-status prose in a PR description as the review authority.
The foreman still rechecks current state before any merge.

Auto-merge has a fixed, conservative policy:

- A recorded Machinist factory run owns the PR, with a successful independent review bound to the implementation and current head. Single-agent deliveries and unrelated PRs are ineligible.
- The agent inspected a complete exact-revision diff and recommended merge. Missing or truncated evidence requires your review.
- Only edits to existing `README.md` and Markdown files under `docs/` qualify, with at most five files and 200 changed lines. Added, deleted, renamed, executable, and symlink files do not qualify. Agent instructions, prompts, security, authentication, deployment, dependency, migration, and public-interface documentation are excluded.
- The PR targets the repository's default branch, is not a draft or fork, and has no private publication hold.
- Checks for the current revision must succeed. Pending, failed, skipped, unknown or incomplete checks block merging. Review conversations must be resolved and no reviewer can have an outstanding change request.
- GitHub branch rules must require every observed check context, an up-to-date branch, and resolved conversations. A check pinned to a GitHub App must come from that App. For example, if CI exposes `Linux checks`, `macOS checks` and an aggregate `check`, all three must be required. Repositories without these rules remain triage-only. Machinist never changes protections or uses a bypass.

The foreman rechecks GitHub before submitting a SHA-guarded squash merge using GitHub's asynchronous API with `bypass_rules: false`. Pending or queued merges are not reported as complete. Later maintenance runs reconcile confirmed merges, including a lost response or restart. Each run checks at most ten recorded pending deliveries for reconciliation. The detailed action report remains in run history.

API definitions use `kind: "repository-maintenance"`; `prompt` is managed by Machinist for this type. Omit `kind` for an ordinary agent job. No new runner image, workflow engine, or agent credentials are required.

## PR risk review demo

Use [the PR risk review prompt](../../examples/automations/pr-risk-review.md) as
an automation named **PR risk review** for your connected repository. Start with
**Manual only**, paste the complete prompt, save, then choose **Run now**. It reads
up to five open PRs and returns their exact revision, risk, reasons and human
approval requirement. It never executes PR code or changes GitHub state.

The selected agent needs an authenticated, read-capable GitHub connector or a
credentialed local/VM runner. A plain Kubernetes sandbox does not receive GitHub
credentials or unrestricted public access. Kubernetes works only when its harness
has a separate authenticated GitHub connector; do not copy credentials into the
pod to make this demo work. Without that connector, use a local or authenticated
VM runner. An access failure must produce a blocker, not a classification.

The policy is zero human approvals for prose-only low-risk changes, one for
medium-risk product changes, and two for high-risk changes. Read the root
[review.md](../../review.md) for Machinist's failure cases and review format.
Markdown that changes commands or agent instructions does not qualify as low
risk merely because it is documentation.

For a video, compare a prose-only README correction, an ordinary UI behavior
change, and a cancellation or permission change. Use real diffs, not labels or
titles. A change to `review.md` itself is high risk because it changes the policy.

This saved automation reports recommendations in the run UI. It does not enforce
approval counts, run automatically on PR events, or authorize zero-human merges.
GitHub rulesets/branch protections remain authoritative. Automated review,
security checks and CI are still needed for low risk. Do not lower existing
protection rules for the demo. Schedule it only after inspecting a manual run.
