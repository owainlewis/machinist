# Configuration

Commands use an executor, optional prompt template, and timeout:

```toml
[commands.audit]
executor = "codex"
prompt_file = "prompts/audit.md"
timeout = "30m"

[commands.custom-workflow]
executor = "custom-workflow-script"
timeout = "2h"
```

Without `prompt_file`, the input prompt is sent unchanged. With a template, include
`{{machinist.prompt}}` for legacy command runs, or task variables such as
`{{task.spec}}` and `{{task.output_dir}}` for workflow tasks. Executors and repositories remain worker-owned:

```toml
[executors.custom-workflow-script]
command = ["./scripts/custom-workflow.sh"]

[repositories.my-project]
path = "/absolute/path/to/my-project"
```

Managed triggers select one command with `command = "audit"`. Model selection remains
available when the executor command includes `{{machinist.model}}`.

## Task workflows

```toml
[workflows.deliver]
steps = ["build"]

[workflows.plan_then_build]
steps = ["plan", { command = "build", approval = "before" }]
```

Each named command must be configured. Workflow prompts can read `{{task.title}}`,
`{{task.source_url}}`, `{{task.spec}}`, and `{{task.output_dir}}`. The shared folder
is restored from the latest completed stage before execution. Keep temporary work
in `MACHINIST_SCRATCH_DIR`. [Storage settings](artifacts.md#storage) control file
limits and retention. Configuration changes apply to new tasks; submitted tasks
keep their saved definitions.

## Migration

The `agents` table was renamed to `commands`. Move `[agents.NAME]` to `[commands.NAME]`
and replace `--agent` with `--command`.

Legacy `[pipelines]` configuration is not supported. Use `[workflows.NAME]` with
an ordered `steps` list for stages tracked by Machinist, or use one script command
when the script should own its internal process. See [the task workflow guide](task-guide.md)
and [workflow configuration](workflows.md). Pre-command databases are
recreated once because this release intentionally consolidates the schema before active use.
