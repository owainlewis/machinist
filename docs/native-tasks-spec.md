# Native first-class tasks

Status: Proposed, 23 September 2026. Revised 23 September 2026: GitHub issue
intake removed; scheduled executions belong to their trigger.

## Outcome

A user can save a task without starting work, edit its requirements, and explicitly
run a workflow when ready. The task retains its identity and history across
executions. Tasks are the only way to request work by hand. A task may link to a
URL, such as a GitHub issue, as background context; the link never defines the
task's identity or execution state.

Example: save “Fix keyboard navigation” as a draft, refine its acceptance criteria,
then run Deliver. If that execution fails, retry within it. If the requirements
change, edit the task and start a new execution. Both executions remain attached
to the same task with the exact briefs they used.

## Current behavior and proposed boundary

Today, `protocol.Task` contains title, spec, and source URL, but `CreateTaskJob`
immediately creates a workflow job. Task inputs are stored against that job.
There is no independently saved request that can own several workflow executions.
GitHub label intake separately polls issues and creates single-command jobs.

Introduce a persistent task above the existing execution machinery:

```text
Task -> workflow executions (existing jobs) -> stage attempts (existing runs)
     -> optional reference URL

Scheduled trigger -> executions (existing jobs, no task)
```

Keep jobs, runs, workflow progression, approvals, and artifact publication. This
proposal extends the task ownership model in [the task/artifact design](task-artifact-model.md);
it does not require renaming those internal execution concepts.

## Scope

Ship native task creation, reading, editing, listing, deletion, explicit workflow
start, and execution history. Support these through the control-plane API and UI.
Existing immediate-submit clients remain supported. Remove GitHub issue intake.

Defer issue tracker integrations of any kind (GitHub, Linear, Jira), issue import,
task dependencies, agent role hierarchies, concurrent executions on one task,
cross-execution artifact handoffs, and a new task-management CLI. Do not add a
separate configurable task status system in this increment.

## Task record

Persist:

- Internal task ID, independent of any execution ID.
- Title and spec (the UI may label spec “Description”).
- Optional default repository, selected before execution if absent.
- Optional reference URL, matching today's `source_url`.
- Monotonically increasing revision, creation time, and update time.

The reference is background context, not a parent relationship or a field that
controls task state. Machinist does not fetch it. Preserve existing input size and
URL restrictions. Saving a native draft requires a nonblank title; its spec may
initially be empty. Neither a workflow nor a connected worker is required to save
or edit it.

Editing requires the expected revision. A stale edit fails with a conflict and
does not overwrite another edit. Increment the revision on successful changes.
Allow edits during execution; make clear that they apply to future executions.
Prior executed briefs remain available in execution snapshots. A complete audit
log of every unexecuted draft edit is outside this increment.

## Starting and repeating work

“Run workflow” selects a repository, configured workflow, and optional model.
Require a nonblank spec for new native executions. A reference alone is not a
saved brief; the user must describe the work.

Starting work must atomically:

1. Check the expected task revision and validate the execution settings.
2. Reject a new start if this task already has an unfinished execution, including
   one waiting for approval or intervention.
3. Create a job linked to the task and freeze its task revision, title, spec,
   reference, selected repository, resolved workflow, and model selection.
4. Queue the first stage through the existing machinery.

All stages and retries read this job's frozen task inputs. Later task edits must
not affect queued stages, prompt rendering, or retries. Preserve the existing
per-attempt artifact bindings. Do not re-read mutable task fields at dispatch.

A start request carries an idempotency key scoped to the task. The native start
API requires it; the UI generates one per start action. Repeating the same request
returns the original execution; reusing its key with different inputs fails.
Revision checks, duplicate handling, and active-execution exclusion must hold
under concurrent requests. Failed validation creates no partial execution.

Retry and request-changes actions retain their existing semantics within an
execution. Running another workflow, or using an edited brief, creates a new job
under the same task after the previous execution becomes terminal. Reuse existing
cancellation behavior where available; do not implicitly abandon unfinished work.

A new execution starts with an empty artifact workspace. Files remain grouped by
execution, with their stage attempts and approval history. Carrying files between
executions requires a future explicit input mechanism.

## Scheduled triggers

Interval and cron triggers keep their current definitions and admission
safeguards. Each occurrence creates an execution owned by its trigger, with no
task. Schedules are not saved requests: their brief lives in configuration, and
creating a task per occurrence would flood the task list. This matches how other
agent products model schedules: a schedule definition with its own run history.

Scheduled executions keep their existing job/run APIs, review actions, and
artifacts. They appear on the Runs page, which remains the list of all executions,
and are not listed on the Tasks page.

## Task presentation

Add a Tasks page that lists tasks, not jobs, and make it the default view. Add
Draft for tasks with no executions. For other tasks, derive the existing Queued,
In progress, Needs attention, or Finished presentation from the latest execution.
Expose its actual outcome; a failed terminal execution must not appear
successful. Finished describes an execution, not a guarantee that every future
version of the request is complete. Keep the Runs page for all executions.

New task offers “Save draft” and “Save and run”. Both use the same task creation
path. If starting fails after saving, retain the draft and show the start error.
Workflow and repository selection are required only for running.

Task detail shows the editable current brief, reference, “Run workflow”, and a
chronological execution list. Selecting an execution shows its frozen instructions,
progress, files, logs, and existing review actions. If the current task revision
differs from the execution revision, show that newer task edits are not included.

Allow deletion only when no execution is unfinished. Confirm that deletion removes
the task and all its executions and artifacts, using existing durable artifact
cleanup. Deletion never touches the referenced URL.

## Removing GitHub issue intake

Remove the `github` trigger family: issue search, label events, permission checks,
acknowledgement labels and comments, and the `github_trigger_requests` table.
Loading a config that still defines `[triggers.github.*]` fails with an error
saying label intake was removed and tasks replace it. Remove the `[github]`
configuration section if nothing else uses it, with the same kind of error.

Historical jobs created by GitHub intake keep their state, runs, artifacts, and
stored issue title and URL for display. They migrate to tasks like other
requested work. Agents may still use GitHub for branches and pull requests; this
change removes only GitHub as a source of task requests.

## API and storage

Add task create/list/get/update/delete operations and a start-execution operation.
Return task IDs and execution IDs as separate, explicit fields. Task detail
includes execution summaries; execution detail retains existing job/run APIs.
Updates and starts accept the expected revision. Return validation errors for
invalid briefs/settings and conflicts for stale revisions or unfinished work.
Use the existing authentication and CSRF protections.

Add a task table and a nullable task foreign key on jobs. Jobs from scheduled
triggers have no task. Keep job-scoped `task_inputs` as the immutable execution
brief; retain `execution_inputs` for stage snapshots. Workers continue receiving
the existing task input shape. Project the saved reference into
`task.source_url` for existing prompt templates.

Preserve legacy job IDs, run IDs, URLs, and artifact access. In particular, the
existing artifact field named `task_id` currently contains a job ID: keep that
legacy meaning and expose the native task relationship separately rather than
silently changing its value.

## Migration and compatibility

Backfill one native task per existing job that was submitted through the API, CLI,
or UI, or created by GitHub intake, and link the job to it. Do not create tasks
for jobs from interval or cron triggers. Prefer existing task inputs; otherwise
use the stored prompt and available title/source metadata (for GitHub intake, the
stored issue title and URL). Generate a deterministic display title when none
exists. Preserve historical inputs, execution state, approvals, and artifacts
without requiring GitHub access. Do not merge historical jobs, even when they
reference the same URL.

Make migration transactional and safe to rerun. Existing immediate-submit API/CLI
creates a native task plus execution through the shared service path and retains
its existing response semantics. Legacy source-only submissions and migrated
source-only work remain executable under their existing rules; the new native
start API requires a saved spec. Single-command jobs remain supported as
executions without a workflow naming migration.

## Acceptance criteria

1. Saving a title-only draft persists across restart and creates no job, run, or
   worker dispatch. It works without a connected worker.
2. A draft can be edited and then started. Starting without a spec or valid
   execution settings leaves it saved and creates no execution.
3. Editing a task while its workflow runs does not change any current or later
   stage's brief in that execution. A subsequent execution uses the new revision.
4. Retrying a stage preserves its execution and task IDs. A fresh workflow gets
   a new execution ID under the same task, retaining both histories.
5. Concurrent edits cannot silently overwrite one another. Concurrent/replayed
   start requests cannot create duplicate or overlapping executions.
6. Scheduled triggers create executions with no task. They appear on the Runs
   page and not on the Tasks page, and their admission safeguards still hold.
7. A config with `[triggers.github.*]` fails to load with a clear message. No
   code path polls or writes to GitHub issues.
8. The UI distinguishes the current brief from executed instructions and groups
   files and approvals under the correct execution.
9. Existing submissions, active workflows, prompt variables, artifact links, and
   review actions continue working after migration, including for historical
   GitHub intake jobs.
10. Deleting an inactive task cleans up all its executions and artifact bytes;
    deleting a task with unfinished work is rejected.

Validate with store/API tests for atomic starts, snapshots, revision conflicts,
scheduled executions without tasks, and migration fixtures (including GitHub
intake and scheduled jobs); add UI coverage for draft save/start and switching
between execution histories. Run the existing workflow, trigger, artifact, and
frontend suites to catch compatibility regressions.

## Delivery order

Each step ships as its own pull request.

1. Remove GitHub issue intake, its table, and its config, with a clear config error.
2. Task persistence, migration, and shared task/start service operations.
3. Native APIs and compatibility adapters for existing submission and scheduled
   triggers.
4. Draft creation/editing, explicit start, execution history, and the Tasks page
   in the UI.
5. Acceptance checks and updates to the task guide and API-facing documentation.
