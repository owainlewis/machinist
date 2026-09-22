# Task, stage, and artifact model

Status: Initial implementation, 21 September 2026. See [Artifact workflows](artifacts.md) for supported configuration and operational limits. The design below also records future spec-editing and adoption behavior.

## Task: the requested work

A task has a title, optional `source_url`, optional `spec`, and a selected
workflow. Require at least one of `source_url` or `spec`.

- `title` names the work for people.
- `source_url` links to the original request, such as a GitHub issue. It is not a
  parent-task relationship or text extracted from a prompt.
- `spec` contains the requested behavior, constraints, and acceptance criteria.
- `workflow` selects how the work is performed.

There is no separate task-level prompt in the target model. Reusable prompts
belong to commands; task-specific instructions belong in the spec. Existing
prompt-based commands remain supported while this model is introduced.

When a spec is supplied, it is the task brief and the source provides background.
When only a source is supplied, the agent reads it for requirements. Consequential
ambiguities require clarification. Record the task inputs used by each execution;
editing the task must not silently change an execution already in progress.

The task template context is `task.title`, `task.source_url`, and
`task.spec`, and `task.output_dir`. These placeholders are supported.

## Workflow: the process

A workflow contains an ordered sequence of steps that reference commands. A
command may run an agent with a prompt or any configured script/executable. A
single agent orchestrating its own subagents is a valid one-step workflow.

Steps advance automatically on successful completion, unless the next step has
an explicit approval gate. Blocked or failed work stops progression. Initial
support does not require dependency graphs, parallel steps, or automatic backward
loops. Commands may own their internal repair cycles.

A stage is a configured step. A stage execution is one attempt at that step.
Retries create new execution records and preserve previous results. The existing
job/run implementation can represent tasks and stage executions without a naming
migration being required first.

## Stage result: outcome, summary, and artifacts

- `outcome`: complete, blocked, or failed; controls progression.
- `summary`: a short human-readable explanation.
- `artifacts`: files produced by the execution, including text and binary files.

A file's existence alone does not establish successful completion. Blocked and
failed executions can still produce useful drafts or diagnostic artifacts.

Planning output does not automatically overwrite the task spec. An implementation
plan describes how to perform the work; a proposed specification is an artifact
until explicitly adopted as the task brief. Preserve the previous spec and its
provenance when adopting a replacement.

There is no single text field underlying all outputs. A future `stage.output`
convenience may resolve a designated text artifact, but artifacts are the storage
model.

## Filesystem boundary and publication

Every stage reads and writes `{{task.output_dir}}`. The worker restores the last
complete snapshot into a fresh runtime-owned directory for each attempt. Its writable
path is supplied to the command. Agents and scripts write normal files there;
they do not need bucket credentials or an upload API.

The worker collects files from that designated directory and publishes them to
the artifact store. The control plane does not reach into arbitrary worker paths.
Do not follow links outside the output directory. Enforce configured size limits.

The worker retains files until publication is acknowledged. Uploads must be
retryable without rerunning the agent and must not create duplicate records.
Only finalized uploads are exposed as available artifacts. Successful completion
requires durable publication of required artifacts; upload failure must remain
visible and prevent advancement. Declare required files with the step’s `required_outputs` list.

Once published, artifacts are immutable. A new attempt produces new artifacts.
The worker's temporary directory is a workspace, not the durable record. Worker
loss before publication may lose unfinished output; this model does not promise
continuous backup of an active workspace.

## Artifact store

Store file bytes separately from their metadata. Each artifact record identifies:

- Artifact ID, task ID, and originating stage execution ID.
- Relative path, content type, byte size, and checksum.
- Storage key, publication time, and availability/expiration state.

A suggested storage layout is:

```text
artifacts/<task_id>/<run_id>/<artifact_id>
```

The run ID identifies one stage attempt. Storage keys are internal; callers use
artifact IDs through authenticated control-plane APIs.

Start with a filesystem store owned by the control plane. A bucket backend can
implement the same interface later. Neither commands nor the UI depend on the
backend. Binary files, including audio, use the same publication mechanism as
Markdown specifications.

## UI access and downstream inputs

The UI lists artifacts with their producing stage and attempt, and provides
authenticated access to the saved copies. Files appear as filename download links, including text, images, and audio. Serve
untrusted artifacts without treating them as executable application content.

Viewing an artifact is separate from approval. An approval refers to a specific
execution and its published artifacts.

Later stages receive the entire latest complete snapshot. Bind its files to
concrete artifact IDs before dispatch; restore them in the shared task folder.
Overwrites and deletions carry forward; earlier immutable snapshots remain in
history. Legacy explicit input mappings remain supported. File contents are not
automatically inserted into prompts.

Snapshot workflow templates when the task is submitted, then resolve artifact
inputs and render each step when it is ready to execute. Save the resolved inputs
with the execution. This changes the current eager rendering of all step prompts.

## Storage configuration and retention

Storage is server-level configuration, separate from workflow definitions. The
following settings are supported. These limits and retention are defaults:

```toml
[storage.artifacts]
backend = "filesystem"
path = "~/.machinist/artifacts"
retention = "30d"
max_file_size = "100MB"
max_run_size = "1GB"
```

The retention clock begins when the task finishes. Preserve artifacts while a
task is active or waiting for approval/input. Explicitly retrying a task protects
its remaining required artifacts while it is active; it cannot recover bytes
already deleted.

Expiration deletes file contents but preserves artifact metadata and execution
history. The UI displays "Artifact expired" instead of a broken download. A retry
or downstream step requiring expired input is blocked with an explanation.

Cleanup must coordinate with task activation and input binding so it cannot
delete an artifact that has become required by active work. Ordinary workspace
cleanup is separate from artifact retention.

## Implementation scope

The initial implementation includes task fields, snapshot templates and inputs,
filesystem storage, worker uploads, authenticated file access, UI downloads and raw-text previews,
shared snapshots and legacy explicit input bindings, required outputs, and retention cleanup.

Bucket backends, task-spec editing/adoption, and automatic publication recovery
after worker restart remain future work. A live worker retries transient transfer
failures without re-executing the agent; permanent errors block progression.
