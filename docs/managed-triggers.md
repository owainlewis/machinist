# Managed triggers

Managed triggers create ordinary control-plane jobs from a GitHub label, a
fixed interval, or a cron schedule. They use the same agent and pipeline
definitions, durable queue, worker matching, and run history as jobs submitted
from the browser or CLI.

## Configure triggers

GitHub repository mappings and triggers belong in the shared Machinist
definition, normally `~/.machinist/config.toml`:

```toml
[github.repositories]
machinist = "owainlewis/machinist"

[triggers.github.issue-intake]
every = "5m"
label = "machinist:requested"
agent = "foreman"

[triggers.interval.repository-audit]
every = "6h"
repository = "machinist"
agent = "audit"
prompt = "Audit this repository for provable bugs."

[triggers.cron.nightly-audit]
schedule = "0 2 * * *"
timezone = "UTC"
repository = "machinist"
agent = "audit"
prompt = "Audit this repository for provable bugs."
```

Repository keys are logical names shared with worker configuration. Each value
must be a unique `OWNER/REPO` slug; slug comparisons are case-insensitive. Keep
credentials, repository paths, executor commands, and other machine-local
settings out of this file.

Each trigger selects exactly one existing `agent` or `pipeline`. A `model` may
also be set when the selected executor supports that model through the normal
model validation. Interval and cron triggers require a repository mapping and a
non-empty prompt. GitHub triggers render the prompt `Complete <issue-url>`.

Trigger names are part of their durable identity:

- `[triggers.github.issue-intake]` is `github/issue-intake`.
- `[triggers.interval.repository-audit]` is `interval/repository-audit`.
- `[triggers.cron.nightly-audit]` is `cron/nightly-audit`.

Machinist validates every mapping and trigger before starting any trigger loop.
An invalid trigger prevents startup and its error includes this identity.

## Authentication

GitHub intake invokes the installed `gh` executable directly and uses its normal
authentication. Log in as a user or GitHub App that can search configured
repositories, read issue label history and collaborator permissions, and update
issue labels. Verify the same environment that runs `machinist start` with:

```sh
gh auth status
```

Do not add a token to `config.toml`. For a service process, provide `GH_TOKEN`
through that process's secret manager or authenticate its `gh` installation.

## GitHub label intake

A GitHub trigger polls once when the server starts, then waits its configured
fixed delay before each later poll. `every` is required and must be from `1m` to
`24h`. One poll considers at most the 100 oldest labelled issues across all
configured repositories.

For each candidate, Machinist checks the label timeline event and the actor who
applied it. It admits only open issues, not pull requests, when the actor
currently has write, maintain, or admin access. It rejects issues from unknown
repositories and events it has already seen.

Admission writes the request event and job to SQLite before changing labels. It
then replaces `machinist:requested` with `machinist:queued`. If that label update
fails, later polls repair the labels without creating a second job. Removing and
reapplying `machinist:requested` after the prior work is terminal creates a new
attempt. Reapplying it while work is active does not create overlapping work for
the issue.

The optional [GitHub Actions comment example](../examples/github-actions/README.md)
turns an authorized issue comment into the intake label. Only the first
non-empty line controls whether the workflow accepts the comment. Comment text
is not used as a job prompt. Edited comments and pull-request comments do not
start work. The example uses a dedicated collaborator token because labels
created with the normal `GITHUB_TOKEN` are attributed to `github-actions[bot]`,
which has no collaborator permission for Machinist to verify.

An opt-in integration test covers both direct-label and comment-to-label intake
against a disposable repository. Install the shipped workflow on that repository,
configure its `MACHINIST_INTAKE_TOKEN`, authenticate `gh` as a write-capable user,
then run:

```sh
MACHINIST_GITHUB_INTEGRATION_REPOSITORY=OWNER/REPO \
  go test ./internal/controlplane -run TestGitHubIntakeDisposableRepository -v
```

The test creates two issues and closes them during cleanup. It is skipped unless the
repository environment variable is set.

## Interval schedules

`every` must be from `1m` to `720h`. The first occurrence is one complete
interval after startup. Later occurrences remain anchored to their scheduled
times instead of drifting with job duration.

## Cron schedules and timezones

Cron schedules have exactly five fields: minute, hour, day of month, month, and
day of week. They support numbers, lists, ranges, steps, and English month and
weekday names. When both day fields are restricted, a time matches when either
field matches, following Vixie cron behavior.

Each cron trigger requires an IANA timezone such as `UTC` or `Europe/London`.
During a daylight-saving jump, a local time that does not exist does not fire.
When a local time repeats, each occurrence fires separately. Seconds,
descriptors such as `@daily`, embedded `CRON_TZ`, and environment assignments
are rejected.

## Admission, overlap, and restart

Interval and cron occurrences use their scheduled UTC time as a durable key.
Each fixed trigger may have only one queued or running job. If another
occurrence arrives while that work is active, Machinist coalesces it instead of
creating overlapping work. It records the coalesced count, skips old backlog,
and advances to the first future occurrence. A failed job does not stop later
occurrences.

If admission itself fails, Machinist records the error and retries that same
occurrence. Failures in one trigger do not stop other triggers.

On restart, an unchanged trigger restores its persisted schedule. Missed time
becomes at most one catch-up job, then the schedule advances to the first future
occurrence. A new or changed trigger starts from the new startup time without
old backfill. Removing a trigger stops future admissions but does not cancel
jobs already admitted.

Trigger loops start only after SQLite and the HTTP listener are ready. Shutdown
cancels polling and waits for the schedulers to stop without admitting new work.

## Status and recovery

The status API and the **Triggers** page show each trigger's identity, family,
next due time, last attempt, last success, active job, candidate and admission
counts, coalesced count, health, and latest error.

Use those fields to choose a recovery action:

- For an authentication or permission error, repair `gh` authentication and
  wait for the same occurrence or label event to retry.
- For a GitHub label-update error, leave the admitted job in place. A later poll
  repairs the labels and does not duplicate the job.
- For a stale fixed trigger, check its active job and worker health before
  restarting the server. Restart recovery preserves unchanged schedules.
- For invalid configuration, correct the trigger named by the startup error and
  restart `machinist start`. No trigger loop starts from a partly valid file.
