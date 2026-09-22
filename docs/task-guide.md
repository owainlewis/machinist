# Your first task workflow

A **task** is the work you want done. A **workflow** is its ordered list of steps.
A step runs a configured agent prompt or script. A **run** is one attempt at a
step; retries and revisions preserve previous attempts.

Start with one build step, or use planning followed by approval and build.
Machinist runs the process you configure; it does not automatically add testing,
code review, or merging to every task.

## Set up planning and build

Build the binary and run `machinist init` as described in the
[quick start](../README.md#quick-start). Authenticate your agent CLI on the worker.
Add a repository to `~/.machinist/worker.toml`:

```toml
[repositories.my_repo]
path = "/absolute/path/to/your/repository"
```

Append these definitions to `~/.machinist/config.toml`:

```toml
[commands.plan]
executor = "codex"
prompt_file = "prompts/plan.md"
timeout = "15m"

[commands.build]
executor = "codex"
prompt_file = "prompts/build.md"
timeout = "45m"

[workflows.build]
steps = ["build"]

[workflows.plan_then_build]
steps = ["plan", { command = "build", approval = "before" }]
```

Create `~/.machinist/prompts/plan.md`:

```text
Task: {{task.title}}
Source: {{task.source_url}}
Requirements: {{task.spec}}

Read the source if needed. Inspect the repository and write a concise plan.
Do not implement yet. Save the plan to {{task.output_dir}}/plan.md.
```

Create `~/.machinist/prompts/build.md`:

```text
Task: {{task.title}}
Source: {{task.source_url}}
Requirements: {{task.spec}}

If {{task.output_dir}}/plan.md exists, follow that plan.
Otherwise read the requirements and linked source.
Implement the change and run relevant checks. Preserve unrelated work.
Save a concise report to {{task.output_dir}}/report.md.
Do not push or open a PR unless the task asks for it.
```

Start the server and, in a second terminal, the worker:

```sh
machinist --config ~/.machinist/config.toml start
machinist --config ~/.machinist/worker.toml worker start
```

Open the address printed by the server. The worker should appear as connected.
Worker paths, credentials and models are configured separately from workflow steps;
see [configuration](configuration.md).

## Create and follow a task

1. On **Tasks**, choose **New task**.
2. Enter what you want done, or paste an issue link. This description is the spec;
   a link on its own becomes the source URL.
3. Choose the repository and workflow. Title, separate source link, and model are optional.
4. Start the task. The board shows Queued, In progress, Needs attention, and Finished.

The **Workflows** page shows the steps in each configured workflow and where it
waits for approval. Its Prompts tab shows the actual configured instructions;
Template help explains task variables. Edit definitions in the configuration
files; this page is a viewer, not a workflow editor.

## Review the result

Open a task to see its current result and progress. Use **Files** for the latest
completed file snapshot, **History** for earlier attempts, **Instructions** for
the original request, and **Details** for execution metadata. History only appears
when there are earlier attempts. Files appears after a completed stage.

Click a text filename to view its raw contents in the UI. Use the download button
for any file. Text previews are limited to 1 MiB; HTML is displayed as text, never
executed. Binary and larger files are download-only.

With `plan_then_build`, planning finishes and the task moves to Needs attention.
Review plan.md, then **Approve and start build**, or **Request changes** and enter
feedback. A revision creates a new planning attempt and returns for review.
Your approval applies to that exact attempt. There is no implicit approval gate
after the final step; configure the gates your process needs.

## Shared files, not temporary working files

Every stage uses `{{task.output_dir}}`. Machinist restores the latest completed
snapshot before a stage runs and saves its updated files after execution.
Overwrites and deletions carry forward; previous snapshots remain in history.
Failed or blocked attempts do not replace the latest completed snapshot.

Only put deliverables and files needed by later stages here. Use
`MACHINIST_SCRATCH_DIR` for temporary clones, helper scripts and raw logs. Scratch
files are not published or carried forward. Python cache files are excluded from
publication. The local folder path can change between attempts; use the template
variable or environment variable rather than saving an absolute path in a plan.

See [artifacts and storage](artifacts.md) for retention, size limits, and legacy
input mappings. Already-submitted tasks retain their original workflow definition.

## Approvals, failures, and merging

Steps advance after reporting completion. A failure or blocker pauses progression;
fix the cause and retry from the task page. After a lost worker or cancellation,
verify the old process stopped before retrying. External actions such as opening
a PR must be reconciled by the command rather than blindly repeated.

The optional [classify-then-merge example](../examples/workflows/risk_delivery/README.md)
builds a PR, asks a separate agent to assess risk, applies a script policy, then
merges or waits for approval. Automatic merging is limited to small low-risk
Markdown/text documentation changes. Other changes require approval. Both paths
require checks and the reviewed commit; changes to the base also require policy
review again. This example requires GitHub credentials with write access and
separate policy executors. It is not enabled by `machinist init`.

For custom scripts, read the [step result contract](workflows.md#step-result-contract).
A successful process exit alone does not complete a workflow stage.
