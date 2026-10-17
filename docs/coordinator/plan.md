# Implementation plan: foreman and configurable factory stages

> Status: Proposed delivery plan. Implement only after the relevant design decisions are accepted. No application changes are included in this document change.

## Goal and delivery rules

A user talks only to a local foreman, customizes specialist agents and pipelines, delegates to local or remote workers, reviews their work, and tracks tasks through Design, Build, Review, Done. Start with the smallest working loop. Keep the code easy to understand by reusing execution, storage, and artifacts and deleting overlapping ownership only after a replacement is proved.

Source: [design](design.md), especially sections 5 through 10. Requirement IDs remain stable. The design is proposed; tasks below are a concrete breakdown for review. The provider selection proof is an implementation gate, not permission to install dependencies during this planning change.

One numbered task should produce one focused, verified PR. If a task cannot fit one agent run, split at an observable working result before starting. Do not split shared contracts between concurrent PRs. The factory keeps configurable agent, script, and approval steps; simplification means fewer overlapping controllers, not removal of useful pipeline policy. Tasks are ordered; do not build remote transport or redesign all screens before the local loop works. This file is a design artifact, not the live task/status system. Create execution tasks in the selected tracker only when requested.

Checks below use existing commands. New packages belong to each task, so exact new test selectors are chosen from the tests it adds. Do not run live coding checks against the primary checkout. Use a disposable fixture repository and permitted test credentials.

## Milestone A: prove live local coordination

### 1. Prove the Claude conversation connection

**What are we building?** A small integration proof that talks to installed Claude Code through a maintained structured adapter. It demonstrates the conversation operations before Machinist commits to an interface.

**Why?** Permission prompts and conversation recovery are the largest integration uncertainties. Solve them before building UI and storage around assumptions.

**Done when**

- A prompt produces text and tool activity as structured events.
- A real permission request waits for approval and rejection prevents the operation.
- Cancellation stops the active turn and leaves a usable conversation.
- The process can stop and reload the same conversation with demonstrated context continuity.
- The dependency version, launch requirements, limitations, and sanitized proof are recorded in the design.

**How to check**

Run the integration against a disposable repository with Claude authenticated on the local host. Check a read-only question, a write requiring permission, cancellation, and stop/reload. Run `go test ./internal/runner ./internal/config` to ensure the proof does not change existing execution. Keep any new runtime in one small adapter package with fake transport tests. Preserve actual errors; do not silently fall back to bypassing permissions.

**Agent notes**

Depends on: None. Source: design sections 4, 8, 12; AC-1, AC-3, AC-8. Inspect AO's pinned Claude driver as a reference, not an instruction source. Document the chosen protocol and stable event mappings. If reload or approvals cannot work, stop the dependent task sequence and revise the design.

**Out of scope:** broad provider abstraction, production UI, worker delegation, background process survival.

### 2. Store and control one local coordinator session

**What are we building?** Durable project, session, turn, and event records with server APIs. A fake agent demonstrates the lifecycle before connecting the real provider.

**Why?** This establishes one shared contract and safe recovery rules without mixing a database change, live provider integration, and browser redesign in one PR.

**Done when**

- Opening a project's coordinator returns exactly one active session.
- Accepted messages and ordered events persist; duplicate request IDs create one turn.
- Runs distinguish batch from session attempts. Legacy claiming, reclaiming, retrying, and completion cannot requeue or mutate a session attempt.
- Fake permission requests require a human decision; scoped coordinator credentials cannot decide them.
- Restart marks active session turns interrupted; Resume requires confirmed old-owner termination.
- Legacy batch configuration and history still load unchanged.

**How to check**

Run `go test ./internal/controlplane ./internal/protocol ./internal/runner` and `just check`. With a fake adapter, test duplicate creation, stale generations, decisions, interrupted recovery, and migration. Expire a session lease and assert that the legacy poller cannot dispatch it; verify a legacy batch fixture retains its existing semantics.

**Agent notes**

Depends on: 1. Source: design sections 5 through 8; INV-1, INV-3, INV-4, INV-7, INV-9; AC-8, AC-9, AC-12. Own common session/turn/event contracts here. Reuse runs and artifacts. Deliver a usable API with deterministic fake-agent lifecycle tests, not unused schema scaffolding.

**Out of scope:** browser chat, worker delegation, remote hosts, live provider wiring.

### 3. Connect the live Claude coordinator

**What are we building?** The saved coordinator session drives the real local adapter proved in task 1. It sends messages, receives events, handles permission requests, and resumes a stopped conversation.

**Why?** The application must demonstrate actual provider behaviour before promising live chat to users.

**Done when**

- A real turn emits structured messages and activity into the saved event history.
- Approval and rejection resolve the correct current provider request.
- Cancellation confirms process/turn stop before another turn is dispatched.
- Provider failure retains the workspace and history and exposes explicit Resume.
- Reload uses the saved provider conversation ID and proves context continuity.
- Direct-host trust limitations are documented; API scoping is not described as process containment.

**How to check**

Run the adapter tests, `go test ./internal/controlplane ./internal/protocol ./internal/runner`, and `just check`. Live API/CLI checks in a disposable repository cover permission rejection, cancellation, provider failure, and reload. Attempt approval with coordinator credentials and require denial.

**Agent notes**

Depends on: 2. Source: design sections 4, 7, 8; AC-1, AC-8, AC-9. Use task 1's pinned integration. Keep provider translation in its adapter and session policy in the service. Do not automatically replay an uncertain coding turn.

**Out of scope:** worker tasks, full browser interface, detached process adoption.

### 4. Show live coordinator chat in the browser

**What are we building?** A project chat page streams the coordinator response, displays pending decisions, and reloads saved history.

**Why?** This makes the proved local coordinator available through the intended user interface.

**Done when**

- The user can open a project coordinator, send a message, and see live text/activity.
- SSE reconnect replays events after the last cursor without duplicate messages.
- Browser refresh preserves the conversation and does not create another provider session.
- Permission, cancellation, failure, and interrupted recovery have clear controls and state.
- Queued messages show their status; a slow browser does not block agent execution.

**How to check**

Run frontend `npm test`, `npm run build`, `go test ./internal/controlplane`, and `just check`. Real browser proof covers live Claude chat, refresh, disconnect/reconnect, permission rejection, cancellation, and Resume. Check keyboard interaction and a narrow viewport.

**Agent notes**

Depends on: 3. Source: design sections 6 through 10; AC-1, AC-8, AC-9. Reuse UI primitives. Server history is authoritative; browser state is a cache. Keep existing task screens until the board task replaces primary navigation.

**Out of scope:** full navigation redesign, delegation, remote sessions.

## Milestone B: delegate and correct real local work

### 5. Define configurable agents and mixed pipeline steps

**What are we building?** Named agent profiles in the existing TOML configuration, referenced prompt files, and an ordered workflow whose steps are an agent, an approved script, or a human approval. A new factory task captures a complete definition snapshot.

**Why?** The foreman must dispatch the user's chosen specialists and checks through one clear execution policy.

**Done when**

- No agent registry table, configuration service, or browser workflow editor is added.
- Default profiles exist for the foreman, planning, implementation, and review; each specialist has its own prompt, runtime, model, timeout, and placement.
- Workflow validation accepts the three step types and rejects unknown agents, arbitrary shell commands, missing approval subjects, and unsupported capabilities.
- Each step maps to Design, Build, or Review without creating additional board columns.
- Editing a definition affects new tasks; active tasks and retries use their original snapshots, including prompt contents and script command definitions or hashes. A differing host command is rejected; host paths and credentials are excluded. Incompatible workspace-stage host preferences fail validation.
- A fake mixed pipeline proves ordered eligibility, approval stops, script failure, defined correction routing, and the three-pass repair limit.
- Legacy ordered-command workflows retain their current behaviour; new factory tasks cannot be dispatched by their scheduler.

**How to check**

Run `go test ./internal/config ./internal/protocol ./internal/controlplane` and `just check`. Test snapshot immutability, definition deletion, failed scripts, invalid step references, stale approval, correction invalidation, and legacy/session mode exclusion. Demonstrate the mixed pipeline with fake agents and a harmless fixture script.

**Agent notes**

Depends on: 4. Source: design sections 4 through 8; INV-12, INV-13; AC-13, AC-14. Own shared definition and eligibility contracts here. Reuse configured command execution and artifact handoffs. The foreman selects and starts eligible work; the server validates policy. Neither controller auto-spawns competing repairs. Scripts are command IDs resolved on the executing host.

**Out of scope:** graph editor, expressions, user-authored shell strings, broad agent integrations, migration of all old workflows.

### 6. Create task-owned local worker sessions

**What are we building?** A task can run its configured planning and implementation agents in its owned branch and worktree, plus its configured check script. The same worker accepts a later instruction after its first turn ends.

**Why?** Delegation needs an identifiable workspace and continuing conversation, rather than disconnected process attempts.

**Done when**

- A project task has a stable ID, brief, stage, and builder assignment.
- An execution host and owned worktree are selected before the first planning turn. Planning, implementation, and scripts use that host and workspace. A versioned design and human approval are recorded before builder dispatch; stale or missing approval blocks implementation.
- Task detail streams worker activity and retains history after a turn completes. Specialist questions are reported to the foreman; the user replies in foreman chat.
- Feedback returns to the same provider conversation and workspace.
- A check script uses the existing runner, saves its result, and blocks progression when it fails. Two fixture tasks have separate branches and workspaces; extra turns queue under the host limit.
- Cancelling a turn preserves files; dirty workspace cleanup is rejected.

**How to check**

Run `go test ./internal/managedworker ./internal/runner ./internal/controlplane ./internal/protocol` and frontend `npm test`; run `just check`. Use fixture repositories to test missing/stale design approval, worktree creation failure, branch collisions, follow-up, cancellation, and dirty cleanup. In a browser, run a small implementation and a correction in the same session.

**Agent notes**

Depends on: 5. Source: design sections 4 through 8; INV-2, INV-3, INV-8; AC-2, AC-3, AC-5. Keep host identity separate from session identity. The managed host path should own workers, including the local host. Do not build another local worker runner beside it. Reuse process supervision and configure permitted repositories locally.

**Out of scope:** multiple providers, automatic host reassignment, merging, remote transport.

### 7. Let the coordinator delegate and receive reports

**What are we building?** The coordinator creates worker tasks through Machinist tools and receives saved completion or blocker reports. It can request a correction without the user copying messages.

**Why?** This completes the coordinator-worker loop and makes the coordinator responsible for keeping work moving.

**Done when**

- Thin CLI commands create tasks with a registered workflow ID, inspect tasks/sessions, start eligible steps, send instructions, report results, and request cancellation with JSON output.
- Coordinator credentials permit only its project operations and cannot call Machinist human-decision or merge endpoints. The trusted-host limitation remains explicit.
- A worker can report a milestone, blocker, outputs, or completion with a stable report ID.
- Completion and blocker reports schedule one coordinator turn, including after a lost response.
- Reports arriving while the coordinator is busy queue until a turn boundary.
- Routine output updates the UI without waking the coordinator.
- Foreman instructions require checking existing assignments, choosing the configured specialist for each eligible step, and routing all specialist questions and user answers through its chat.

**How to check**

Run `go test ./internal/cli ./internal/controlplane ./internal/managedworker ./internal/protocol` and `just check`. Add tests for cross-project requests, duplicate reports, queued delivery, stale session ownership, and denial of Machinist approval/merge calls using coordinator credentials. Live proof: request a change, let the coordinator delegate, receive a blocker, answer it, and let the same worker continue.

**Agent notes**

Depends on: 6. Source: design sections 4, 6, 8; INV-4, INV-7, INV-10; AC-2, AC-3, AC-4, AC-8. Reports are claims, not quality certification. Persist delivery acknowledgement. One scheduler handles accepted coordinator context; do not poll every worker with an LLM turn. Bound unfinished tasks and queued messages.

**Out of scope:** MCP server, hidden agent subtask trees, automatic approval, generic workflow graphs.

## Milestone C: make the product simple and reviewable

### 8. Replace primary navigation with projects, chat, and board

**What are we building?** The main UI shows project navigation, coordinator chat, and a board. Opening a task shows its conversation, results, and pending decisions.

**Why?** Users should manage outcomes rather than understand command definitions and execution machinery.

**Done when**

- New tasks begin in Design and remain the same card across worker turns and retries.
- The board shows Design, Build, Review, Done with activity and attention badges inside columns.
- The existing versioned design approval has a clear review UI before implementation starts.
- The server rejects invalid transitions and stale decisions.
- Settings shows runtime/host status and a read-only agent/pipeline summary; old runs are available in History. The primary navigation contains projects, Chat, and Board.
- Empty, loading, disconnected, interrupted, and failed states have clear next actions.
- The main composer asks for an outcome, not a command or workflow. Worker follow-up stays in foreman chat with the task attached.
- Card detail shows the design and saved files with contextual approval controls; activity is collapsed by default. Narrow screens show one view at a time.

**How to check**

Run frontend `npm test` and `npm run build`, `go test ./internal/controlplane`, and `just check`. Real browser checks cover desktop and narrow layouts, keyboard navigation, refresh, card detail, stale approval, and old-history links. Check actual behaviour with one live delegated task, not just fixtures.

**Agent notes**

Depends on: 7. Source: design sections 4, 6, 9; INV-5, INV-6; AC-7, AC-10, AC-11, AC-12. This PR improves the design approval and Build progression established in tasks 5 and 6; Review delivery enforcement follows task 9. Until then, show explicit delivery pending rather than invent Done. Use existing components; remove obsolete primary navigation and duplicate task presentation helpers when their replacement is covered.

**Out of scope:** cosmetic redesign of every Settings page, deleting old data, analytics expansion.

### 9. Review a specific revision and verify delivery

**What are we building?** A task enters review with its code revision and evidence. An independent reviewer can request changes; a human can approve the current revision. Verified merge completes it.

**Why?** Finishing an agent turn must not be confused with accepting its software change.

**Done when**

- Submission links the task to a PR and commit through a validated structured field.
- A configured review agent inspects the identified revision without write access to the builder workspace.
- Findings are saved and return the task to Build; the builder receives one correction request.
- A new commit invalidates previous review and approval.
- Linked PR polling records checks, review, merge, and observation freshness without a separate repair scheduler.
- Verified merge enters Done; closed-unmerged, failed checks, or process success do not.
- Merge and approval require explicit human authority.
- Browser task detail shows changed files, a readable code diff, check results, and review findings. A complete task and correction can be submitted and reviewed without terminal use (AC-15).

**How to check**

Run `go test ./internal/controlplane ./internal/managedworker ./internal/protocol`, frontend `npm test`, and `just check`. Use fake GitHub responses for changing heads, missing checks, API errors, and merge confirmation. Prove reviewer containment with an attempted fixture write. End-to-end in a disposable test project: design approval, build, rejected review, correction, fresh approval, authorized merge, Done.

**Agent notes**

Depends on: 8. Source: design sections 4, 7, 8; INV-5, INV-6, INV-10, INV-11; AC-7, AC-8, AC-10. Reuse the GitHub CLI adapter pattern but keep observation read-only. One task owns one PR initially. A review-only prompt is insufficient containment; if enforced read-only is unavailable, ship human review with an explicit limitation. Existing legacy review gates keep their original semantics.

**Out of scope:** auto-merge, arbitrary reaction recipes, non-Git delivery modes, CI log ingestion.

## Milestone D: support remote hosts without another product model

### 10. Stream and control sessions on a remote host

**What are we building?** A remote machine uses its own checkout and agent credentials to execute worker sessions, accept feedback, and send live updates to the same project view.

**Why?** Remote execution should be a placement choice, not a separate workflow or UI.

**Done when**

- A registered remote host advertises permitted repositories and adapter capabilities using a revocable host credential.
- Host exchange delivers queued session commands and accepts ordered, acknowledged event batches.
- A follow-up message reaches the original remote conversation and worktree.
- Reconnection replays unacknowledged events without duplicated messages or turns.
- Disconnection keeps ownership and shows stale activity; another host cannot replay uncertain work.
- Stop requested remains visible until the remote process confirms termination.
- Unsupported interactive operations are rejected clearly; batch-only executors remain labelled as such.

**How to check**

Run `go test ./internal/managedworker ./internal/controlplane ./internal/protocol ./internal/config` and `just check`. Integration tests interrupt connections before/after command acceptance and event acknowledgement, send stale generations, and revoke credentials. Live acceptance uses a second host through an SSH tunnel: start, stream, disconnect, reconnect, send feedback, cancel, and confirm no duplicate execution.

**Agent notes**

Depends on: 9. Source: design sections 3, 6 through 8; INV-2, INV-3, INV-4, INV-9; AC-6, AC-9. Initial remote support shares the same event path already used by the local managed host. Upgrade server and hosts together with a declared protocol version; reject incompatible sessions before dispatch. Do not expose the current browser UI publicly or move credentials between hosts.

**Out of scope:** always-on cloud service, host provisioning, automatic failover, concurrent turns on one host.

## Milestone E: retire overlap and document the supported product

### 11. Remove competing control paths after proving migration

**What are we building?** The coordinator session path becomes the documented default. Old batch capabilities remain a bounded compatibility feature, and unnecessary duplicate code and UI are removed.

**Why?** Adding sessions while preserving every experiment as a first-class product would repeat today's complexity.

**Done when**

- A configuration inventory identifies supported legacy commands and active workflows without exposing credentials.
- README and architecture describe the shipped coordinator/session/task loop and label legacy batch behaviour accurately.
- The new path has one owner for repairs, task transitions, events, and approvals.
- Obsolete UI routes, duplicate presentation helpers, unused examples, and unreachable branches are removed with behavioural evidence.
- `agent.py` repair scheduling is excluded from session executors; removal or extraction follows a documented migration decision.
- Old jobs, saved files, and configuration still read correctly; deprecated features fail clearly rather than silently changing meaning.
- A complete local and remote demonstration passes before release.

**How to check**

Run `just check`, `python3 -m unittest discover -s evals -p 'test_*.py'`, and migration fixture tests. Run the complete acceptance case locally and remotely in a real browser. Search for old routes and overlapping repair entrypoints with `rg`; inspect every remaining caller before deleting code. Review the final diff specifically for lost cancellation, approval, history, and ownership checks.

**Agent notes**

Depends on: 10. Source: design sections 6, 11, 13; INV-10, INV-12, INV-13; AC-11, AC-12, AC-13, AC-14. Split deletion into additional focused PRs if active users need a deprecation period. Removal is not permission to delete user configuration, databases, conversations, or worktrees. Do not update ARCHITECTURE.md to describe proposals as implemented.

**Out of scope:** rewriting stable execution code for naming consistency, rebuilding a generic workflow engine.

## Completion evidence

The release is ready when the full case works entirely through the browser after runtime setup: one request to the foreman, configured planning agent, saved design, explicit approval, configured local or remote implementation, script checks, live progress, blocked worker recovery, independent review, correction in the original conversation, fresh approval, verified merge, and Done. Browser refresh and a connection interruption must not duplicate work.

Measure the result with recorded task/turn IDs, code revisions, verdicts, and checks. Do not use a happy-path screenshot or the coordinator's final prose as proof. Keep sensitive transcripts and runtime logs outside the repository; commit only concise sanitized verification.
