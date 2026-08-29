# Configuration

Machinist separates portable workflow definitions from machine-local execution
settings.

| File | Owns | Default path |
| --- | --- | --- |
| Machinist definition | server, agents, prompt paths, pipelines | `~/.machinist/config.toml` |
| Worker configuration | executors, model aliases, repositories, token paths | `~/.machinist/worker.toml` |

`--config` names the primary configuration for each command. It selects the
Machinist definition for `machinist start` and the worker file for `machinist run`,
`machinist submit`, or `machinist worker start`. For direct runs, select a separate
Machinist definition with `--machinist-config`.

## Limit concurrent managed jobs

By default, every available worker may lease a different job. Set a positive global
limit when the control plane should run fewer jobs at once:

```toml
[server]
max_concurrent_jobs = 1
```

The limit counts jobs, not individual pipeline steps. Additional submissions remain
in the durable queue. An active pipeline keeps its slot between steps, and an expired
lease can be redispatched without consuming another slot. Lowering the value does not
cancel active work; the control plane starts no additional queued jobs until the active
count falls below the new limit. Restart the control plane after changing this setting.

## Define an agent

Agents live in the Machinist definition:

```toml
[agents.foreman]
executor = "codex"
prompt_file = "agents/foreman.md"
timeout = "120m"

[agents.audit]
executor = "codex"
prompt_file = "agents/audit.md"
timeout = "60m"

[agents.shepherd]
executor = "codex"
prompt_file = "agents/shepherd.md"
timeout = "120m"
```

Prompt paths are resolved relative to the Machinist definition. Every prompt must
contain the supported work-request parameter:

```text
{{machinist.prompt}}
```

Machinist replaces every occurrence byte-for-byte with the `--prompt` value. It
does not recursively expand parameters in user input. The final rendered prompt
is limited to 512 KiB.

## Schedule Shepherd

Shepherd is a separate, managed agent for an opt-in pull request merge queue. Add one
schedule per worker repository name:

```toml
[shepherd.api]
repository = "api"
every = "30m"
max_actions = 3
```

`every` must be at least one minute and `max_actions` must be positive. The limit counts
every GitHub mutation, including creating the repository label definition, creating or
editing comments, updating a branch or pull request, pushing a repair, resolving a thread,
and merging. The control plane queues the first run when it starts, then persists the next
due time in SQLite. It never overlaps two Shepherd runs for one repository. If a run reaches
its action limit, the next run inventories GitHub again and continues from live pull request
state and audit comments. Before a stacked parent merge, Shepherd records the child's
pending retarget so a later run cannot mistake the child for independent work. With
`max_actions = 1`, recording the transition, merging the parent, retargeting the child, and
marking the transition complete happen in separate runs.

Schedules use the `agents.shepherd` definition and managed workers, because repository
paths and GitHub credentials remain machine-local. Restart the control plane after changing
a schedule.

Shepherd treats the pull request label `machinist:auto-merge` as its sole permission to
update, repair, comment on, or merge a pull request. At the start of each run, Shepherd
checks that the repository defines this label and creates the definition when it is absent,
using one action from that run. It never applies the label itself. Apply it only to pull
requests you want Shepherd to change:

```sh
gh pr edit <number> --add-label "machinist:auto-merge"
```

An unlabelled pull request remains inventory-only. A repository policy may apply the label
to Dependabot patch and minor updates. Keep major updates unlabelled unless a person opts in
that specific pull request.

## Configure an executor

Executors live in the worker file because commands and execution environments
are machine-specific:

```toml
[executors.codex]
command = ["codex", "exec", "--json", "--model={{machinist.model}}", "--sandbox", "danger-full-access", "-"]
models = { luna = "gpt-5.6-luna", terra = "gpt-5.6-terra", sol = "gpt-5.6-sol" }

[executors.claude]
command = ["claude", "--print", "--verbose", "--output-format", "stream-json", "--model={{machinist.model}}", "--dangerously-skip-permissions"]
models = { haiku = "haiku", sonnet = "sonnet", opus = "opus" }
```

Machinist sends the rendered agent prompt to the command's standard input and uses
the target repository as its working directory. Standard output and error remain
live.

The example commands grant broad permissions because the shipped agents use
GitHub and worktrees outside the current repository. Change them to match your
own prompts and trust boundary. Provider credentials come from the executable's
own configuration or the worker process environment; they are not fields in
`worker.toml`.

The shipped Codex command uses its JSONL event stream so Machinist can read the
final `turn.completed` usage. Machinist records input tokens plus output tokens;
cached input is already included in the input count and is not added again.
Machinist automatically adds `--json` to recognized `codex exec` commands, so
existing worker configurations collect usage after upgrading. Codex executor
names such as `codex-local` may place the `codex` executable behind an
argv-preserving wrapper. Renamed Codex executables are recognized when invoked
directly, through `env`, or through `mise exec`/`mise x`; the command must still
contain the Codex `exec` argument.
The shipped Claude command uses `stream-json` so Machinist can read the terminal
`result` usage. Recognized Claude Code `--print` commands are normalized to that
format when no output format is configured. Claude usage totals input,
`cache_creation_input_tokens`, `cache_read_input_tokens`, and output tokens once
each. Explicit `text` output is left unchanged and can use the token-usage file
fallback; explicit `json` and `stream-json` output are parsed directly.
Other executors can report a non-negative integer through the
`MACHINIST_TOKEN_USAGE_PATH` file exposed to every run. Missing or malformed
usage remains unavailable and does not affect executor output or completion.

## Select models per task

The optional `{{machinist.model}}` parameter belongs in one complete optional
argument such as `--model={{machinist.model}}`. If a run omits `--model`, Machinist
removes that argument and lets the executable use its default.

When an executor declares aliases, callers must choose one of those aliases:

```sh
machinist run --agent=foreman --model=terra \
  --repo=/path/to/repository \
  --prompt="Complete https://github.com/example/project/issues/123"
```

Machinist resolves `terra` through the selected executor before starting the
process. Model-selected pipelines must use one executor so the alias has one
unambiguous meaning.

## Create a pipeline

Add agent definitions and list them in execution order:

```toml
[agents.lint]
executor = "codex"
prompt_file = "agents/lint.md"
timeout = "20m"

[agents.test]
executor = "codex"
prompt_file = "agents/test.md"
timeout = "30m"

[pipelines.quality]
agents = ["lint", "test"]
```

Then run the pipeline by name:

```sh
machinist run --pipeline=quality \
  --repo=/path/to/repository \
  --prompt="Check the current repository"
```

Machinist runs each agent independently, in order, with the same input. It stops
at the first unsuccessful process.

## Register repositories for managed work

Direct mode accepts any repository path through `--repo`. Managed workers expose
only the named repositories in their worker file:

```toml
[repositories.api]
path = "/absolute/path/to/api"

[repositories.web]
path = "/absolute/path/to/web"
```

The browser and server use the names `api` and `web`. Only the worker sees the
local paths.

## Use explicit files

Pass both configuration files when you do not want the defaults:

```sh
machinist run --agent=foreman \
  --config=/absolute/path/to/worker.toml \
  --machinist-config=/absolute/path/to/config.toml \
  --repo=/absolute/path/to/repository \
  --prompt="Complete https://github.com/example/project/issues/123"
```
