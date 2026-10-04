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
check script to your repository's checks. Add projects in the browser, or keep
existing `[factory.projects]` entries. Set `github` to `owner/repository` when
you want PR delivery tracking. Keep the existing `[server]` configuration. Factory scripts are fixed
operator-configured argument arrays, never shell strings submitted from chat.

```sh
./bin/machinist start --config ~/.machinist/config.toml
```

Open `http://127.0.0.1:7331/`. No separate managed worker is needed for local
factory sessions. Existing batch commands still use their existing workers.
Factory mode is opt-in. Its History view shows previous batch runs read-only;
new work uses the foreman and project board. Finish any batch runs that need
browser actions before switching modes. To operate existing batch approvals,
retries or cancellation in the legacy interface, set `factory.enabled = false`
and restart the server. Batch commands remain available in either mode.

A global `claude-agent-acp` executable is discovered on PATH. Source development
falls back to `internal/agent/runtime` under the server's working directory.
An explicit `[factory.hosts.local] acp_command = ["/path/to/claude-agent-acp"]`
overrides discovery. Missing runtime errors are shown in the conversation.
Machinist never installs dependencies during an agent turn.

## Add a project

Choose **Add project** in the browser, then select:

- **Existing folder:** an absolute Git repository root that already exists on the
  selected host and contains at least one commit.
- **Clone from Git:** an HTTPS or SSH Git URL and an absolute destination folder.
  Machinist clones once, then reuses that checkout. It never replaces an existing
  folder. An existing checkout is accepted only if its origin matches exactly.

**Browse folders** opens a directory picker on the selected host. Navigate to a
repository and choose **Use this folder**, or enter an absolute path directly.
For Git clones, choose the parent folder; the destination adds the repository
name and remains editable. Folder browsing never creates or changes files.

Execution defaults to this computer. Select a configured remote host to pin the
project and its task workspaces to that machine. Remote paths are paths on that
host. Remote credentials and build tools must already be installed there. Git
URLs cannot contain passwords or HTTPS user credentials; use the host's normal
Git authentication. GitHub delivery tracking is inferred from a GitHub origin;
other Git hosts remain usable without GitHub PR tracking. Explicit `github` values
use two nonempty safe components, `owner/repository`, with ASCII letters, numbers,
dots, hyphens or underscores; query strings and fragments are not allowed.

Browser-added projects are saved in the existing Machinist SQLite database.
Configured project IDs take precedence over stored entries after restart.
Project IDs use ASCII letters, numbers, dots, hyphens or underscores; `.` and `..`
are not valid IDs. Display names can contain spaces and other characters. A Git
project with a missing checkout can clone again before creating a new task.
Existing files, local changes, and a different origin are never overwritten.
Each task gets its own worktree and branch, not a full clone.

## Daily use

The composer shows the configured foreman agent, runtime, and model. An omitted
model is shown as **Default model**; Machinist does not guess which model the
runtime selects.

Task detail has **Design**, **Changes**, and **Checks** tabs. Designs render as
documents; changes are grouped by file with a filter and line numbers. Failed
checks appear first, with logs available on demand. Approval controls stay visible
while reading. Resize or expand the panel; its width and selected tabs are saved
in the browser. Permissions and interrupted agents remain available across tabs.

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
`machinist_command`. The project path is a POSIX absolute path on the remote host, such as
`/srv/project`, regardless of the operating system running the browser server.

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
Never resume uncertain remote work on another host. An agent process exiting is
not pipeline completion. If it exits without an accepted structured report, the
foreman is notified and the task pauses for explicit human recovery. Delivery
turns must link the published PR; an unlinked exit also pauses for human recovery.

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
