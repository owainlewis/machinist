# Browser factory

Talk to one local foreman. It creates tasks, delegates planning and implementation,
runs configured checks, and brings the work back for human approval. Tasks keep
one card through Design, Build, Review, and Done. The normal coding loop runs in
the browser.

## Setup

Install and authenticate Claude Code on each execution host. Install the pinned
structured adapter once, using Node 22 or later:

```sh
npm install -g @agentclientprotocol/claude-agent-acp@0.85.1
```

For source development you can instead use:

```sh
npm ci --prefix internal/agent/runtime --ignore-scripts
just build
./bin/machinist init
```

Copy the `[factory]` sections in [the example](../examples/factory.toml) into your
Machinist config. Copy its `factory-prompts` directory beside that config. Set the
project path to your Git repository, set `github` to its `owner/repository` if you
want PR delivery tracking, and replace the check script with your repository's
checks. Keep the existing `[server]` configuration. Factory scripts are fixed
operator-configured argument arrays, never shell strings submitted from chat.

```sh
./bin/machinist start --config ~/.machinist/config.toml
```

Open `http://127.0.0.1:7331/`. No separate managed worker is needed for local
factory sessions. Existing batch commands still use their existing workers.
An existing configuration without `factory.enabled = true` keeps the legacy UI.

A global `claude-agent-acp` executable is discovered on PATH. Source development
falls back to `internal/agent/runtime` under the server's working directory.
An explicit `[factory.hosts.local] acp_command = ["/path/to/claude-agent-acp"]`
overrides discovery. Missing runtime errors are shown in the conversation.
Machinist never installs dependencies during an agent turn.

## Daily use

1. Select a project and describe the desired change in foreman chat.
2. The foreman creates a task-owned branch and worktree, then starts planning.
3. Open the design when asked. Approve it or request changes in the browser.
4. The builder works in that task's workspace. File and shell permission requests
   appear in task detail. Inspect each request before allowing it.
5. Configured scripts check the committed revision. Inspect the diff and check
   output before choosing Approve delivery. Request changes to return to the same
   builder. Approval lets that builder publish the approved revision and link its
   PR when you ask the foreman to deliver it.
6. A linked PR stays in Review until its approved revision is verified as merged.
   Machinist observes GitHub every 30 seconds for unfinished linked PRs, and you
   can refresh manually. It does not merge changes.

The foreman is the only chat composer. Worker transcripts are available in task
detail; discuss a task with the foreman to send feedback to its worker. Routine
activity is collapsed. A completed agent turn is not a completed coding task.
Projects without a linked PR stay in Review after approval; they cannot claim
verified GitHub delivery.

## Agents and pipelines

Agents are named TOML profiles with prompt files, a Claude runtime, optional model,
and timeout. Pipelines are ordered agent, script, and human approval steps.
Settings shows their configuration; there is no workflow editor or agent registry.
Editing prompts or steps affects new tasks. Existing tasks retain their resolved
prompt and command contents. Host or repository changes cannot silently move an
existing task to another machine.

The current release uses **human code review** in place of the configured review
agent. Claude plan mode alone does not enforce a read-only machine boundary.
The review step presents the submitted diff and checks to the human and retains
the code approval gate. Planning and implementation use the configured agents.

## Remote execution

The foreman stays local. A project's configured host runs its workspace steps
through SSH using the remote host's own Claude authentication. The example has a
commented remote configuration. Install the same Machinist version and pinned
adapter on that host. Configure `ssh`, the remote `acp_command`, and
`machinist_command`. The project path is absolute on the remote host.

`tools_port` defaults to 7332. It must be an unused loopback port on that host.
SSH reverse forwarding connects the remote agent's scoped tool bridge to the
local server. SSH must permit reverse forwarding; use normal SSH keys/config and
host verification. No public browser endpoint or shared agent credentials are
required. The first release serializes factory turns across hosts.

## Recovery and limits

Refresh reloads saved messages and events. A server restart marks unfinished
turns interrupted and does not replay them. Before Resume, confirm the old process
has stopped. SSH cancellation stops the local connection but cannot prove the
remote process is dead. Inspect the remote host before confirming recovery.
Never resume uncertain remote work on another host.

A project permits four unfinished tasks. Turns queue across conversations; wait
or stop before sending another instruction to a busy conversation. After three
repairs, the task pauses for human review. Continue after review permits another
attempt in the same workspace. Cancellation preserves files and history.
Workspaces are retained; this release does not automatically delete them.

## Trust boundary

The browser binds to loopback. Human decisions require the browser's same-origin
CSRF token. Short-lived conversation credentials permit only the assigned
project/task tools and cannot approve or merge. The existing batch worker token
cannot approve factory work. Machinist tools do not accept arbitrary file paths,
commands, or task-state values.

These API rules do not sandbox a trusted local agent process. Agents can use
host credentials and permitted shell tools. Worktrees separate files and branches,
not shared Git configuration, network services, or credentials. Use repositories
and execution hosts you trust. Do not treat provider permission modes as OS
containment.

Runtime state and transcripts stay in SQLite and host-local workspaces, outside
this source repository. See [architecture](../ARCHITECTURE.md) and
[adapter proof](../internal/agent/README.md) for implementation and verification.
