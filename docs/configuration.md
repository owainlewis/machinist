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
limits and location. Configuration changes apply to new tasks; submitted tasks
keep their saved definitions.

## Artifact storage directory

Files are stored on the control-plane server’s disk, separately from the worker’s
scratch directory. Choose a persistent directory in the server config:

```toml
[storage.artifacts]
path = "/var/lib/machinist/artifacts"
```

The default is an `artifacts` directory beside the server database. Relative paths
resolve against the config file; `~` expands to the server user’s home. The server
creates the directory and must have permission to write there. Restart the server
after changing storage settings. When relocating existing storage, stop the
server and copy the entire artifact directory before changing the path; changing
the setting alone does not move saved files. Back up the database and files together.
See [storage settings](artifacts.md#storage) for size limits. Files are kept until their task is deleted.

## Migration

The `agents` table was renamed to `commands`. Move `[agents.NAME]` to `[commands.NAME]`
and replace `--agent` with `--command`.

Legacy `[pipelines]` configuration is not supported. Use `[workflows.NAME]` with
an ordered `steps` list for stages tracked by Machinist, or use one script command
when the script should own its internal process. See [the task workflow guide](task-guide.md)
and [workflow configuration](workflows.md). Pre-command databases are
recreated once because this release intentionally consolidates the schema before active use.

GitHub issue intake was removed. A config that still defines `[triggers.github.NAME]`
fails to load; delete those tables and submit work directly. `[github.repositories]`
stays, because interval and cron triggers use it. Upgrading drops the
`github_trigger_requests` table; jobs it created keep their history.
