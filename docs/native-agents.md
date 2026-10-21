# Native agent runs

New runs in the web UI use `native-v1`: one primary run owns all observed child activity. The harness discovers repository definitions in the task worktree:

- Claude Code: `.claude/agents/*.md`.
- Codex: `.codex/agents/*.toml`, with `name`, `description`, and `developer_instructions`.

The Agents page shows the configured primary prompt template, model choice, and native definitions for the selected local repository. Model inheritance is explicit. Built-in and plugin-provided agents are managed by the harness; remote repository definitions are reported as unavailable rather than inferred from a local checkout. This view is read-only and has a Refresh action.

Commit those files to the repository. Machinist does not translate or copy agent definitions. The primary instructions come from `commands.primary.prompt_file` in the server config. The example config and `machinist init` include this command. Set its executor to `codex-native` or `claude-native`. Set a model only when it is available to the signed-in account. Codex checks `account/read` and `model/list` before starting a primary thread; the service can still reject a listed model at turn start.

Host workers use existing CLI subscription sign-in. Native adapters remove inherited desktop routing variables and API-token variables from the child environment. Codex requires a ChatGPT account; Claude rejects an explicit API key source. This is a personal host integration, not approval for hosted commercial subscription use.

`codex-app-server` owns one stdio JSON-RPC process per run. The configurable command must launch `codex app-server`; model selection travels in `thread/start`. The worker authorizes the assigned repository. Codex must already trust its main repository path in the CLI configuration. A linked worktree inherits trust from that Git common repository root. The adapter rejects project-configuration-disabled warnings instead of silently running without repository agents. Register only repositories whose configuration you trust. CLI sandbox settings and permissions remain in the configured command. `claude-stream` requires Claude print mode with `stream-json`; it must not use `--bare`, which disables subscription authentication. Ordinary process commands and existing CLI exec workflows still work.

Each run stores `native-input.json` locally, with the rendered primary prompt, adapter, argv, model, definition hash, and hashes of relevant repository configuration. Raw stdout/stderr remain in `events.jsonl`. Normalized child observations and primary/child messages share that spool. Unknown native events remain in raw output. Only primary completion and primary usage determine the run result. Child failures are observations the primary can recover from.

Workers send bounded, lease-authenticated event batches to `POST /api/v1/runs/{id}/events`. Sequence IDs are contiguous; identical replay is acknowledged, and conflicting payloads or stale leases are rejected. An acknowledged cursor is saved beside the worker spool. SQLite stores activity separately from the lifecycle journal. `GET /api/v1/runs/{id}/events?after=N` returns up to 128 events and the next cursor. The UI polls this API and displays connection loss with the last observations. Completion waits for pending event delivery. Workers checkpoint both the acknowledged sequence and spool byte offset, so long runs do not repeatedly parse already-uploaded logs. Older numeric checkpoints remain readable. Timeout, cancellation, or a stale lease stops the owned process group and enumerated descendants, including tools in separate process groups. Each execution also gives tools a random inherited ownership marker so normal reparenting after the harness exits does not lose cleanup ownership. A failed descendant scan or kill is reported as cleanup failure rather than confirmed shutdown. Interrupted runs mark unfinished child observations interrupted; successful primary completion without a child completion signal leaves that child's status unknown.

## Reading agent activity on a run page

Open a run to see its **Overview**, **Agent reports**, **Primary log** and **Details** tabs. When the task has been retried, an **Attempts (N)** tab appears before **Details**.

The **Agents** section of the overview lists only agents that ran in this attempt, not every definition in the repository. The primary agent appears on its own line above the cards. Each card shows:

- The role the harness reported for that agent, with underscores and hyphens shown as spaces and the first letter capitalised (`code_reviewer` becomes Code reviewer). Repeated roles are numbered, such as Reviewer 1 and Reviewer 2. These are agents that ran, not required pipeline stages.
- Its status: Working, Complete, Failed, Interrupted, Cancelled or Status unknown. Status unknown means the agent never sent a completion signal before the harness output ended. It does not mean the agent or the run succeeded.
- A short outcome: the first paragraph of the agent's own report, cut to about 150 characters. It is an excerpt, not an approval or success verdict. A completed reviewer may still have requested changes.

Select a card (it ends with **Read report**) to open that agent's full report in the **Agent reports** tab. You can switch between agents there. The full report is the authority; the overview does not repeat it.

Claude roles come from the `subagent_type` on native Agent/Task calls. Codex roles come from the child's `thread/read` metadata (`agentRole`), separately from its task name. When no role was recorded, the report says "The harness did not record this agent’s configured role." The card is labelled "Subagent" and says "Role not reported". If Codex recorded an agent path, the card uses the last part of that path as its label and says "Agent name recorded; role unavailable"; that label is a name, not a role. An unknown role means only that the role is missing. Machinist does not guess a role, such as triage or review, from the agent's name or report. Read the full report to see what the agent did. Existing Claude roles can be recovered from retained raw calls; older Codex histories may retain only a name.

The primary returns one JSON object:

```json
{"outcome":"complete","summary":"Implemented and verified the change","delivery":{"pr_url":"https://github.com/owner/repo/pull/123","head_sha":"0123456789012345678901234567890123456789","branch":"codex/change"}}
```

Outcomes are `complete`, `blocked`, or `failed`. Omit `delivery` for an answer with no file changes. Code delivery still requires a clean task worktree and matching local branch, pushed SHA, and PR evidence. PRs are never merged automatically. External publication holds remain enforced.

Native reviewer feedback is advisory. This mode does not enforce the legacy specification approval gate or create the legacy isolated read-only review checkout. Use a legacy pipeline if those enforced gates are required. Historical recipes, journal replay, existing tasks, and saved prompt overrides remain readable. Legacy APIs retain their omitted-recipe behavior; callers opt in with `recipe: "native-v1"`. New host automations use native mode when the primary command is configured; saved legacy automations retain their recipe.

Leases remain in memory. After a server restart, activity history survives, but a running process is not automatically resumed or duplicated. Existing manual-stop-before-retry handling still applies. Native mode is currently host-only; Kubernetes keeps its existing delivery contract and does not advertise this capability.

Checks:

```sh
go test ./internal/config ./internal/runner ./internal/managedworker ./internal/controlplane ./internal/cli
cd internal/controlplane/web && npm test && npm run build
MACHINIST_NATIVE_LIVE=1 MACHINIST_CODEX_LIVE_MODEL=ACCOUNT_SUPPORTED_MODEL go test ./internal/runner -run TestNativeLiveRepositoryAgents -v -count=1
```

The live test uses a temporary Git worktree and the host's existing login. It verifies repository child discovery using a value present only in the child's definition. Codex uses a temporary trusted configuration and a read-only symlink to the existing login; the user's saved configuration is unchanged. No API keys are used. Set `MACHINIST_NATIVE_LIVE_EXTENDED=1` to check two repeated, parallel child identities, live activity before completion, and missing-file recovery in both harnesses. The deterministic tests cover framing, parallel identities, child failure, primary-result isolation, unknown events, cancellation, timeout, durable replay, sequence conflicts, and stale leases.

Protocol references: [Codex app-server](https://learn.chatgpt.com/docs/app-server), [Codex custom agents](https://learn.chatgpt.com/docs/agent-configuration/subagents#custom-agents).

## Cancellation and graph verification (2026-10-21)

Cancellation immediately revokes execution. For two minutes, that same worker instance and lease token may upload the remaining events and acknowledge process shutdown. This permission cannot renew execution or apply an agent result. A persisted shutdown acknowledgment permits retry without another manual stop assertion, and never carries into the next attempt. A shutdown acknowledgment is independent of agent success, so a completion racing cancellation cannot publish work or revive the cancelled job. The acknowledgment and terminal events survive journal replay. The cleanup permission, like execution leases, is in memory; a server restart during cleanup or an expired permission leaves shutdown unconfirmed and requires operator verification.

The UI polls while cleanup is pending and distinguishes stopping from unconfirmed shutdown. Stop confirmation is carried from the runner before cancelled post-run checks can return early.

Claude activity accepts only `local_agent` starts and subsequent events for known agent IDs. Shell tasks remain diagnostic output. Original delegation names remain stable; current progress is separate. Codex interactions do not reset child lifecycle state, and the primary thread is excluded from child activity. Existing graphs reconstruct identity classification and original names from retained raw output, without deleting history. Older histories without explicit identity evidence retain their normalized fallback; unknown task types are not inferred as agents for new runs.

Real host probes used the existing subscriptions and one native child running a uniquely tagged 120-second Node timer. Cancellation was requested only after its process was present. Neither probe required manual cleanup:

| Harness | Run | Stop observed after cancel request | Server / local spool / acknowledged events |
| --- | --- | --- | --- |
| Claude | `run_4632bf55775e256ae8565542` | 7.258 seconds | 56 / 56 / 56 |
| Codex | `run_4f0e8dce63dcda5d6bf4ac62` | 0.729 seconds | 82 / 82 / 82 |

Both included `run.completed` on the server and a persisted stop acknowledgment. Timing includes waiting for the worker's next heartbeat and is not a termination-latency benchmark. An unrelated product preview remained healthy. Historical replay reduced the bootstrap graph from twelve identities to three real agents, the repair run from twenty-five to four, and the Codex acceptance run from four to three. The corrected bootstrap graph was also checked in the browser.

Verification: full `go test ./...`, focused `go vet`, 73 UI tests and production UI build passed. Regression checks cover separate-session descendants with an unrelated sentinel, enumeration failure, cancellation before launch, worker stop-confirmation retention, expired/wrong cleanup tokens, completion racing cancellation, durable stop replay, stable labels and historical classification. Descendant enumeration is host cleanup, not an OS isolation boundary against tools deliberately stripping inherited ownership or other same-user processes. Shared-host process authority remains a separate limitation.
