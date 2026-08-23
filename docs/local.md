# Run Factory locally

This guide starts one control plane and one worker on macOS or Linux.

## Requirements

- Go 1.25.13 or newer on the 1.25 release line, or Go 1.26.6 or newer
- Git
- `curl`
- `just`
- at least one authenticated Pi, Codex, or Claude Code CLI
- an authenticated `gh` CLI for centrally managed GitHub repositories

Node.js is not required for normal startup.

## Platform support

Factory supports macOS and Linux. The server, worker, and local launcher are
tested on both Apple Silicon and Intel macOS workers. There are no intentional
macOS feature gaps.

Windows is not supported. The launcher and worker lifecycle depend on Unix
process signals, executable permissions, and shell tools.

## Configure the control plane

Most control-plane configuration is stored in SQLite and managed through the
browser. The optional `~/.factory/config.toml` contains only bootstrap settings
needed before SQLite opens:

```toml
listen = "127.0.0.1:7337"
database = "server/factory.sqlite3"
default_build_runtime = "codex"
```

Relative database paths resolve from the config file directory. Unknown fields,
symlinks, and files larger than 1 MiB are rejected. Command-line flags override
the file. Copy [the example](../examples/config.toml) only when changing these
defaults.

The control-plane database contains prompts, results, and repository metadata.
Factory creates the database, its marker, and SQLite WAL and shared-memory files
with owner-only permissions (`0600`). When a valid existing database is opened,
Factory corrects broader permissions before use and rejects non-regular files or
symlinks in these locations. The containing database directory must be a real
directory owned by the server's effective user and must not be writable by group
or other users. Factory validates the configured path before creating missing
directories, then separately validates its resolved target. Path components and
symlinks must be owned by the effective user or root; group or world-writable
ancestors require sticky-bit protection. This permits protected system symlinks
such as macOS `/var` without trusting links controlled by another user. Existing
`0755` database directories are accepted; use `chmod go-w PATH` before startup
when an explicit database directory is group or world writable.

## Configure a worker

Build the binaries and copy the example:

```sh
just build
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
```

Edit `~/.factory/worker.toml`:

```toml
server = "http://127.0.0.1:7337"
name = "local-codex"
runtime = "codex"
runtimes = ["pi", "codex", "claude-code"]
# Optional. Defaults to 10 and accepts values from 1 through 100.
max_concurrent = 10
```

One Worker is a pool for its configured coding agent runtimes. Each slot runs
an independent Pi, Codex, or Claude Code session with its own worktree and
process group. Preparing an attempt also consumes a slot. Choose a lower value
when local CPU, memory, or provider limits require it.

The optional legacy `runtime` field is the default for older clients that do
not select a runtime. Existing single-runtime files continue to work without a
`runtimes` field. New configurations can list several runtimes and may omit
`runtime`, in which case the first listed runtime becomes the primary one.
Factory reports each one as ready, missing, unauthenticated, or unhealthy, and
the Worker stays available while Git and at least one configured runtime are
ready.

Factory migrates existing SQLite databases to the expanded worker capacity
range when the control plane starts.

With this `~/.factory/worker.toml` filename, Factory defaults durable worker
state to `~/.factory/workers/worker`. The config filename, rather than `name`,
selects the state directory.

No worker repository list is required. Factory detects local GitHub access with
`gh auth status` and clones centrally managed repositories on demand. Optional
legacy repository paths remain available for manual UI delegation. Relative
paths resolve from the worker TOML directory, and each path must be a real,
non-bare Git checkout with an `origin` remote. Factory starts legacy work from
the origin default branch without changing the checkout. To use another base,
configure it under that repository:

```toml
base_branch = "release/2026.07"
```

To run only one coding agent, keep the existing single-runtime form, such as
`runtime = "claude-code"`. Set `data_directory` only when an explicit relative
or absolute override is needed; never share one data directory between Worker
identities.

## Start

The launcher builds the Go binaries, starts one control-plane process, waits for
health, starts the worker, and waits for that worker to register. The server
runs every provider and schedule Automation evaluation loop:

```sh
just run
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337).

Stop both processes with Ctrl+C.

The build also installs a unified command beside the compatibility binaries.
It can start either existing process without changing its configuration or
signal behavior:

```sh
~/.factory/bin/factory server start
~/.factory/bin/factory worker start
```

Use `--config PATH` with either `start` command to select the corresponding
server or Worker TOML file. In another terminal, finite commands read only the
loopback API:

```sh
~/.factory/bin/factory status
~/.factory/bin/factory status --cursor CURSOR
~/.factory/bin/factory show RUN_ID
~/.factory/bin/factory workers
```

Add the global `--json` option for one JSON value on stdout. Set
`FACTORY_SERVER=http://127.0.0.1:PORT` or pass `--server URL` when the operator
API does not use the default port. These commands never open SQLite or Worker
directories.

Admit one or more work items with one command:

```sh
~/.factory/bin/factory build \
  https://github.com/OWNER/REPOSITORY/issues/123 \
  https://github.com/OWNER/REPOSITORY/issues/124

~/.factory/bin/factory build \
  --repo github.com/OWNER/REPOSITORY \
  LINEAR-123 LINEAR-124
```

Use `--runtime`, `--request-key`, `--rebuild`, or `--wait` when needed. An
opaque reference requires `--repo`. Without `--repo`, a GitHub-only batch may
span several enabled managed repositories. The server admits the complete
batch or none of it. `--wait` exits 2 when any Work needs input, 0 when every
Work is ready, no-change, or legacy succeeded, and 1 when terminal Work includes
a failure or cancellation.

When `--request-key` is omitted, the CLI stores an uncertain admission under
`~/.factory/operator/admissions` before sending. A lost response or failed
output keeps that key for the next identical command. The journal uses
owner-only permissions and retains at most 100 pending requests.

Add a GitHub repository to the central fleet once:

```sh
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  -d '{"remote_identity":"github.com/OWNER/REPOSITORY"}' \
  http://127.0.0.1:7337/api/v1/repositories
```

List the current fleet with `GET /api/v1/repositories`. Set
`{"enabled":false}` with `PUT /api/v1/repositories/REPOSITORY_ID/enabled` to
stop new routed work without interrupting an execution whose worker assignment
is already frozen. Posting a repository first discovered from a legacy worker
promotes it into the enabled central fleet. Reposting a centrally managed
repository does not override an explicit disable.

To start with a different Worker config:

```sh
just run ~/.factory/claude-worker.toml
```

To run more than one Worker, start the control plane once and then start each
additional Worker directly:

```sh
~/.factory/bin/factory-worker \
  -config ~/.factory/claude-worker.toml
```

## Delegate a task

The manual delegation screen lists both optional legacy checkouts advertised by
the selected worker and every repository in the central catalog. An enabled
central repository is selectable when that worker is online, healthy, reports
the required GitHub access, and can acquire managed repositories. Factory
reserves it for that worker when the task is created. Repositories that are
disabled or unavailable remain visible with the reason. Add a
`[repositories.<key>]` entry only when delegation must use an existing local
checkout.

In the UI:

1. Open Workers and confirm the worker is online and healthy.
2. Select Delegate task.
3. Enter a title and description.
4. Select the worker and repository.
5. Submit.

The Runs view shows the Session state. Session detail shows attempts, lifecycle
events, results, and errors.

The same operation is available through the API:

```sh
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  -d '{
    "request_key": "manual-example-1",
    "title": "Review the README",
    "description": "Review the README for errors, fix them, test the change, and commit it.",
    "worker_id": "WORKER_ID",
    "repository_id": "REPOSITORY_ID"
  }' \
  http://127.0.0.1:7337/api/v1/tasks
```

Worker and repository IDs are available from:

```sh
curl --fail --silent --show-error \
  http://127.0.0.1:7337/api/v1/workers
```

## Data and overrides

Factory stores state below `~/.factory` by default. Common overrides:

```text
FACTORY_DATA_HOME
FACTORY_SERVER_CONFIG
FACTORY_WORKER_CONFIG
FACTORY_BUILD_DIR
FACTORY_LISTEN
FACTORY_SKIP_BUILD
FACTORY_WORKER_READY_SECONDS
```

Examples:

```sh
FACTORY_LISTEN=127.0.0.1:7444 just run

FACTORY_DATA_HOME=/srv/factory \
  just run /srv/factory/worker.toml
```

The server remains loopback-only.

## Back up and restore the control plane

Use the server's backup mode instead of copying a live SQLite file. Backup mode
uses SQLite's online snapshot operation, includes every committed transaction
from the WAL, validates the result, writes files with mode `0600`, and exits. It
does not interrupt a running control plane, create a missing source, or apply
migrations to the live database.

```sh
install -d -m 700 /secure/factory-backups/2026-08-05
~/.factory/bin/factory-server \
  -backup /secure/factory-backups/2026-08-05/factory.sqlite3
```

The command uses the database selected by `FACTORY_DATA_HOME`,
`FACTORY_SERVER_CONFIG`, or `-database`, just like normal startup. A successful
backup contains these two required files:

```text
factory.sqlite3
factory.sqlite3.v2-control-plane
```

The live `factory.sqlite3-wal` and `factory.sqlite3-shm` files are temporary and
must not be copied or restored. The backup command folds committed WAL data into
the standalone snapshot. Restore rejects a backup with either sidecar present.
Both commands refuse writable destination directories and never overwrite an
existing database, marker, WAL, or shared-memory path.

Configuration is not stored in the database snapshot. Copy the active server
`config.toml` and every worker TOML file into the protected backup directory.
Also retain any external secrets or service configuration used to start the
process. Keep their existing restrictive permissions. Worker identity and
retained-worktree state remain under each worker's configured data directory;
back them up separately when the recovery plan must preserve those local paths.

### Restore runbook

Restore requires downtime and a fresh destination:

1. Stop the control plane and all workers. Keep the old Factory data home
   unchanged so rollback remains possible.
2. Install the backed-up configuration into a new mode-`0700` Factory data
   home. Update absolute paths only when the recovery host differs.
3. Restore with the same Factory version that made the backup, or a newer
   compatible version:

   ```sh
   install -d -m 700 /srv/factory-restored
   FACTORY_DATA_HOME=/srv/factory-restored \
     ~/.factory/bin/factory-server \
     -restore /secure/factory-backups/2026-08-05/factory.sqlite3
   ```

4. Start the control plane with `FACTORY_DATA_HOME=/srv/factory-restored`.
   Restore applies supported migrations in a private staging directory and
   compares the complete resulting schema with the binary before publishing the
   destination. It rejects a backup made by a newer schema, a corrupt database,
   a forged or incomplete migration ledger, a missing or invalid marker, or any
   existing destination.
5. Check `curl --fail http://127.0.0.1:7337/healthz`, then inspect retained task
   attempts and events, Workflows, Automations, and managed repositories in the
   UI. Start workers and confirm they register as healthy before enabling new
   work.
6. Keep the old data home and backup until the restored system has completed a
   representative task. To roll back, stop the restored processes and restart
   the unchanged old data home with its original binary and configuration.

The restore command runs SQLite quick and foreign-key integrity checks before
publishing the destination. The health check confirms that normal startup then
opens the restored database. Never restore over an existing home and never
combine a main database copied by hand with WAL or shared-memory files from a
different point in time.

## Migrate a legacy factory-poller

Migration is offline and disabled-first. Never run it while any legacy poller
process can write the ledger.

1. Stop every `factory-poller` process and keep it stopped.
2. Back up the legacy `poller.toml` and ledger.
3. Start the current Factory control plane and open **Automations → Migrate
   legacy poller**.
4. Select the legacy paths when they are not the defaults, confirm the poller is
   stopped, and choose **Preview locked snapshot**.
5. Review every resolved source path, managed-repository identity and ID,
   proposed Workflow and Automation title, observation count, and error.
   Unsupported command queues
   remain recoverable from the later archive but are not imported. If the
   ledger contains observations for a queue removed or renamed in
   `poller.toml`, Factory shows that ledger-only queue and blocks Import. Restore
   the matching queue entry, stop the poller, and run Preview again so no
   observation identity is silently omitted.
6. Choose **Import disabled Automations**. Factory verifies the exact Preview
   snapshot while holding an exclusive legacy-ledger lock. Existing submitted
   task identities and deleted-task tombstones are retained. Imported
   Automations cannot be enabled yet.
7. For every legacy pending observation, choose **Resume** to replay its exact
   durable task request or **Skip** to record that it must not dispatch.
8. Choose **Finalize and archive**. Factory locks and verifies the same snapshot,
   then archives consistent copies of the config and ledger with a hash
   manifest. It does not modify or delete either source file.
9. Review and test each typed Automation, then enable it. The control plane now
   owns evaluation and deduplication.

Preview, Import, and Finalize fail closed if the source paths, config bytes,
ledger bytes, inode, schema or rows, snapshot digest, or lock availability
changes. Correct the cause and run a new Preview. An archive write failure
leaves the migration imported and safe to retry. Closing the browser or
restarting Factory rediscovers the active imported migration and preserves
pending Resume or Skip decisions and an already completed archive.

## UI development

Only contributors changing the UI need Node.js. Use Node.js 22.22.2 or newer
on the 22 release line, Node.js 24.15.0 or newer, or Node.js 26 or newer:

```sh
just ui-install
cd web && npm run dev
```

Before committing UI changes:

```sh
just ui-check
just ui-build 0
```

The operator build embeds the committed `web/dist` and never invokes npm.

## Troubleshooting

`127.0.0.1 refused to connect`

- confirm `just run` is still running;
- inspect the terminal for server or worker startup errors;
- check `curl http://127.0.0.1:7337/healthz`;
- check that another process is not using port 7337.

Worker never becomes healthy

- confirm the selected runtime command is on `PATH`;
- authenticate Codex or Claude Code as the same OS user;
- confirm every repository path and its `origin`;
- ensure each worker has a unique data directory;
- inspect the worker JSON logs.

GitHub Automation reports `gh_missing` or `gh_unauthenticated`

- install `gh` on the control-plane host;
- run `gh auth login` as the same OS user that starts `factory-server`;
- verify `gh auth status --hostname github.com`;
- restart the server or test the Automation again.

Legacy poller migration cannot acquire its lock

- confirm every old poller process is stopped;
- confirm no SQLite inspection tool has a transaction open on the legacy
  ledger;
- do not copy or edit the source files during Preview, Import, or Finalize;
- retry the action after releasing the writer.

Legacy poller snapshot changed

- leave imported Automations disabled;
- inspect what changed in the config or ledger;
- return to the migration dialog and run a new Preview against the stable
  source. Factory does not partially import or archive a changed snapshot.

A worktree is retained

Factory keeps worktrees when they are dirty or may contain unpublished work.
Open the assigned Worker to see retained paths and cleanup commands. Use the
attempt ID from the Session detail or retained worktree card to preview cleanup:

```sh
~/.factory/bin/factory-worker cleanup ATTEMPT_ID \
  --config ~/.factory/worker.toml
```

Add `--confirm` to remove the worktree. The local branch is preserved, but
uncommitted changes shown in the preview are lost.
