# Artifact workflows

A task describes the work. A workflow chooses the commands. Each stage attempt
produces files, which the worker uploads to control-plane storage before the
workflow advances.

## Configure planning and build

```toml
[commands.plan]
executor = "codex"
prompt_file = "prompts/plan.md"

[commands.build]
executor = "codex"
prompt_file = "prompts/build.md"

[workflows.deliver]
steps = [
  { command = "plan", required_outputs = ["spec.md"] },
  { command = "build", approval = "before" },
]
```

Step IDs default to command names. Use `id = "another-plan"` for repeated commands.
Stages share `{{task.output_dir}}`. The latest complete snapshot is fixed when
the consuming attempt is created, before approval.

`prompts/plan.md`:

```text
Task: {{task.title}}
Source: {{task.source_url}}
Requirements: {{task.spec}}
Read the source if needed. Write the implementation plan to
{{task.output_dir}}/spec.md.
```

`prompts/build.md`:

```text
Implement the plan in {{task.output_dir}}/spec.md.
The original requirements are {{task.spec}}.
Verify the change and open a PR. Save useful reports in {{task.output_dir}}.
```

The agent must also write the [workflow result](workflows.md#step-result-contract).
Recognized Codex/Claude executors receive those instructions automatically.
Scripts and other wrappers must implement the result contract themselves.

## Submit

```sh
machinist --config worker.toml submit --workflow deliver --repo my-repo \
  --title "Add export support" \
  --source-url https://github.com/acme/my-repo/issues/42 \
  --spec "Export the filtered results as CSV."
```

The UI presents the same fields. Supply a source URL, a spec, or both. Planning
files remain outputs; they do not overwrite the submitted spec.

## Runtime contract

- `MACHINIST_OUTPUT_DIR`: published task files for this attempt; only intended deliverables belong here.
- `MACHINIST_SCRATCH_DIR`: private temporary work (clones, helper scripts, caches, raw logs); not uploaded or passed to later stages.
- `{{task.output_dir}}`: shared task files, restored into this attempt’s local directory.
- `MACHINIST_STEP_RESULT_PATH`: JSON outcome and summary, separate from outputs.

Regular files in the output directory are collected after the process exits,
except Python caches (`__pycache__`, `.pyc`, `.pyo`),
including output from failed/blocked executions. Symlinks and special files are
rejected. Limit: 1,000 files per attempt. Inputs are downloaded and checksum-verified
before execution. Templates render once; braces in task text remain literal.

Temporary transfer failures retry with backoff while the worker maintains its
lease. The agent is not rerun. Permanent upload errors block the task; local outputs
remain available for inspection. Worker loss or restart before publication does
not automatically resume publication; the task becomes interrupted and needs
operator recovery. This is not continuous workspace backup.

Uploads are idempotent by run/path/content. Different bytes under the same run/path
are rejected. Completion must include all published files and every required output before advancing.

## Storage

```toml
[storage.artifacts]
backend = "filesystem"
path = "~/.machinist/artifacts"
max_file_size = "100MB"
max_run_size = "1GB"
```

Default storage is `artifacts` beside the server database. Relative paths resolve
against the server config file. Size units are binary multiples (KB = 1,024 bytes).
Settings apply at startup.
Back up the database and artifact directory together. Changing the path does not
migrate existing files. Filesystem is the initial backend; buckets remain future work.

Files live under task/run/artifact IDs. SQLite stores paths, MIME types, sizes,
SHA-256 checksums, timestamps, and deletion tombstones. Files are kept until you
delete their task. There is no time-based expiry. Cleanup runs periodically after
task deletion and retries failed file deletions.

## UI and API access

Task detail presents the latest complete snapshot in Files and older outputs in History.
Files download through authenticated requests. Files expired by older server
versions retain their metadata and display an expiration label.

- `PUT /api/v1/runs/{run_id}/artifacts?path=spec.md`: raw bytes; worker bearer token,
  `X-Machinist-Instance`, and `X-Machinist-Lease` headers.
- `GET /api/v1/jobs/{task_id}/artifacts`: metadata.
- `GET /api/v1/artifacts/{artifact_id}/content`: bytes, including range requests.

Reads require the worker bearer token or a same-origin application request with
its CSRF header. This uses the existing local control-plane trust model, not
per-user access control. Responses use attachment and sandbox headers. The UI previews supported text files as escaped raw text (up to 1 MiB); active HTML is never rendered. Binary and larger files are download-only.

## Shared task folder

Every new workflow shares task files automatically, including scripts using only
`MACHINIST_OUTPUT_DIR`. In prompts, use `{{task.output_dir}}`. Planning can write
`{{task.output_dir}}/plan.md`; build reads that same path. No `inputs` mappings
are needed. Before each stage, Machinist downloads the latest completed stage's
entire snapshot into a fresh local output folder. After execution it uploads a
new snapshot. The physical path may differ between workers or attempts.

Successful snapshots carry overwrites and deletions forward. Failed/blocked
attempts remain in history but do not replace the last completed snapshot.
Snapshots preserve older versions and avoid relying on a worker's temporary disk.
Files and raw-text previews are available on the task page; previews are limited
to text files up to 1 MiB, with downloads for larger or binary files.

Already-submitted tasks retain their snapshotted workflow definitions, including
legacy input mappings. For new submissions, remove `inputs` from workflow config
and read files by name from `{{task.output_dir}}` instead. `{{stage.output_dir}}`
is accepted as a compatibility alias for that same folder.
