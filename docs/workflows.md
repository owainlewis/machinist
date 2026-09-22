# Workflows

For a complete setup and a walkthrough of the UI, start with [your first task workflow](task-guide.md).

A workflow is an ordered list of existing commands. One agent can deliver an
entire change, including its own subagents, or separate commands can perform
triage and implementation. Commands can run agent prompts or scripts.

```toml
[workflows.deliver]
steps = ["task-to-pr"]

[workflows.checked_delivery]
steps = ["triage", "build"]

[workflows.approved_delivery]
steps = ["triage", { command = "build", approval = "before" }]
```

The command names must exist under `[commands]`. A workflow has 1–32 steps.
The ordered list is the chain: there are no dependency declarations, automatic
backward loops, or parallel steps. Commands own their internal repair policy.

## Run a workflow

Use the web job composer to choose a workflow and repository, then supply the
issue URL or request. Or submit it to the control plane:

```sh
machinist submit --workflow=checked_delivery --repo=my-repo \
  --source-url=https://github.com/acme/my-repo/issues/42
```

Workflows currently run through the managed control plane and worker. Direct
`machinist run --command=...` and existing command submissions retain their
process-exit semantics. Scheduled triggers still select commands.

Tasks have an optional title, source URL, and spec; provide a source or spec.
Commands reference `{{task.source_url}}` and `{{task.spec}}`. Existing
`{{machinist.prompt}}` templates receive the combined task brief.

Workflow templates are saved at submission. Each attempt snapshots the task and
the shared-folder snapshot; the worker renders local paths at execution.
Editing configuration does not change a submitted workflow.
See [Artifact workflows](artifacts.md) for passing files between stages.

Legacy `--prompt` workflow submissions are accepted as task specs. They use the
same file collection and template rendering as other tasks.

## Step result contract

A workflow command must write one JSON object to the path in
`MACHINIST_STEP_RESULT_PATH` before exiting:

```json
{"outcome":"complete","summary":"PR https://github.com/acme/my-repo/pull/57 is ready for review. Tests passed."}
```

`outcome` and `summary` are required; `approval_required` is an optional boolean. The summary must be nonempty and the file
must be at most 16 KiB. Outcomes:

- `complete`: start the next step, or complete the workflow.
- `blocked`: pause and display what the operator needs to resolve.
- `failed`: stop and display the failure.

A process failure always stops progression, even if it wrote `complete`. Exit
zero without a valid result also fails. A process can exit zero and report a
blocked task: the process result and task result are distinct.

Recognized Codex and Claude CLI executors receive appended instructions explaining
the result-file contract. Other agent wrappers must include equivalent instructions
in their own prompts. The executor must be allowed to write the result path.
Scripts receive their original stdin unchanged and write the file themselves:

```sh
issue=$(cat)
# Inspect or perform the work for "$issue" here.
printf '%s\n' '{"outcome":"complete","summary":"Verification passed"}' \
  > "$MACHINIST_STEP_RESULT_PATH"
```

`MACHINIST_JOB_ID` stays constant across steps and retries. `MACHINIST_RUN_ID`
identifies this execution attempt. Do not treat result summaries as proof of
quality; configure the actual verification and review your workflow requires.

## Approval and recovery

Steps advance automatically. `approval = "before"` pauses before a particular
step. Its **Approve and start** action appears in the job detail view alongside
previous results. Retrying a step with an approval gate requires approval again.

**Cancel task** stops queued progression and asks a running workflow process to
stop when its next heartbeat is rejected (normally within ten seconds when the
server is reachable). A network partition may delay termination. Cancelled jobs
require confirmation that the process stopped before retrying.

Blocked and failed steps offer **Retry**. Earlier successful steps are preserved,
and the retry is a new execution in the history. Update requirements in the issue
before retrying a blocked triage step.

If a workflow worker loses its lease, the job becomes **interrupted**, not queued
for automatic replay. Confirm the previous process has stopped before explicitly
retrying. The command must reconcile existing Git/GitHub effects: a crashed build
might already have opened a PR. Machinist does not resume an agent conversation or
promise exactly-once external actions.

All steps are pinned to the first worker's configured name to preserve access to
its workspace. Use a stable, unique worker name; do not run multiple machines with
that name. The worker must support all executors/models in the workflow. There is
no automatic cross-worker recovery. Upgrade the server and workers together;
older workers do not receive workflow jobs.

The job page shows planned steps, results, and attempts. Jobs waiting for approval,
input, or interruption recovery appear in **Needs attention**. Workflow completion
means its commands reported completion, not that Machinist independently certified
a PR or merged it.

See [the staged example](../examples/workflows/staged/config.toml) and its prompts.

## Request changes during review

An approval gate before a stage reviews the immediately preceding completed stage.
The UI offers **Approve and start …** or **Request changes**. Requesting changes
requires feedback and creates a new attempt of the stage being reviewed. It gets
its original task and command, the previous attempt's summary and saved files,
and the feedback. Earlier review feedback is retained across successive revisions.
The task spec itself is unchanged.

After a successful revision, a fresh approval gate is created. Its downstream
inputs reference the revised artifact IDs. The old gate is superseded; a stale
browser cannot approve it. Previous attempts, outputs, and feedback remain in
history. A failed revision can be retried without losing review context.

The existing syntax is unchanged: `approval = "before"`. An approval before the
first stage authorizes starting work and has no previous result to revise, so it
only offers approval. This feature does not add a final-result approval gate.

API: `POST /api/v1/jobs/{id}/request_changes` with `run_id` (the current approval
gate's run ID) and nonempty `feedback` (up to 16,000 UTF-8 bytes), using the same
authentication as approval. Revision-capable workers advertise `reviews: true`.

### Conditional approval

A trusted stage may return `{"outcome":"complete","summary":"Review required","approval_required":true}`.
The control plane then pauses before the next stage and persists that gate for
retries and restarts. This can add an approval requirement; it cannot bypass a
configured `approval="before"`. The final stage has no subsequent action to gate.

See [classify then merge](../examples/workflows/risk_delivery/README.md) for an
issue-to-PR pipeline with independent risk assessment and a deterministic merge
policy. Only small low-risk documentation changes qualify for automatic merging.
