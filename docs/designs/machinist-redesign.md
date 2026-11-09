# Machinist Redesign: Assign Work, Get Pull Requests

*   **Author:** Owain Lewis
*   **Status:** Proposed
*   **Date:** 2026-11-09

--------------------------------------------------------------------------------

## Overview

Machinist will become one control plane that a team runs, with any number of runners
that do the work on Kubernetes or on a local machine or VM. The control plane does not
care where a run happens.

Anyone with write access to a connected GitHub repository can mention Machinist on an
issue. A developer can also assign work from the UI or from a Claude Code or Codex
session through MCP. Each route creates a **ticket**, Machinist's own record of one piece
of work.

For each ticket, a runner starts the team's existing coding agent in a fresh clone of
the repository, from a shared playbook. The agent decides how to do the work, using its
own subagents, and uses the GitHub CLI to push and open a pull request. A fresh reviewer
then reads the PR and comments on it. Machinist tracks the ticket, limits how long the
agent can go without a person, brings it feedback, and records the result.

The same engine runs scheduled automations, such as "review open PRs every 48 hours and
merge the safe ones". An automation is a schedule, an agent and a prompt.

**Terms.** A **harness** is the coding agent program, Claude Code or Codex. A **run** is
one start of a harness. A **playbook** is the instruction file for an agent. The
**factory** is the directory of playbooks, reviewers and automations.

This design replaces the current job modes, control planes and configuration layout. It
supersedes the earlier product design.

## The Problem

**A developer who starts a coding agent has to stay with it.** They steer it, answer it
and judge the result. On a team, each person can use different tools, prompts and review
habits, so the team has no shared process and no shared record of what agent work costs
or whether it was accepted.

**Machinist does not solve this today.** The evidence is its own use and design:

*   **It is rarely used.** The home install has recorded 10 jobs, 6 of them for one todo
    application. Only 10 of 571 commits are authored by Machinist. These are lower bounds.
*   **A team cannot use it.** The server refuses to listen on anything but loopback, and
    the actor is hard-coded to `local`, so it cannot attribute work to a person.
*   **Work is mostly entered by hand.** It has schedules and PR follow-ups, but no way for
    a teammate to assign work from where they already are.
*   **It does not record outcomes.** It records tokens and duration, but not whether a PR
    was merged or how often a person had to step in.
*   **It is larger than its job.** It has about 19,700 lines of non-test Go. Much of that
    exists to pass work between stages and to keep GitHub access away from agents.

**How it works today.** Two separate control planes exist. `Server` uses a SQLite job
store. `GitHubFactoryServer` stores a job journal as GitHub comments and replays it to get
current state. Both hand runs to a worker process over HTTP. Five job recipes (factory,
direct, answer, single, native-v1) decide which stages run. Agents have no GitHub access:
they return a Git bundle, and a separate publisher pushes it using GitHub App credentials.

## Goals

*   **G1. Assignment from GitHub.** A mention in a comment by a user with write access
    creates one ticket and an acknowledgement on the issue within 2 minutes. Processing
    one comment twice creates one ticket.
*   **G2. Assignment from a coding session or the UI.** A developer can pass an issue
    reference or ticket text through an MCP tool or the Assign box. The call returns a
    ticket ID without waiting for the work. A retried call creates one ticket. MCP tools
    also report status, answer a question and cancel a ticket.
*   **G3. One engine.** Assigned work and automations use one path: run an agent in a
    clone, then run any configured reviewers. The Go code has no stage routing or role
    routing, and knows no agent or reviewer by name.
*   **G4. An independent opinion on each ticket's PR.** By default, one fresh reviewer
    reads the first PR of a ticket and posts findings as a PR comment. It is advisory. A
    team can remove it or add more.
*   **G5. One GitHub login.** Every run uses the GitHub CLI. The agent pushes and opens
    PRs itself. The control plane logs a warning at startup when a default branch has
    no protection or the login has admin rights.
*   **G6. Limited turns and time.** A ticket stops and waits for a person after a set
    number of agent runs with no human input. Each run has a deadline. Spend is capped
    by the provider's own limits.
*   **G7. Team credentials.** Runners use service accounts, API keys, federation or cloud
    providers, through the variables each harness already reads. The setup check shows
    which credential source the harness actually used.
*   **G8. One control plane, N runners.** One control plane serves any number of runners.
    A run behaves the same on a Kubernetes runner and a local runner, and the control
    plane makes no decision based on which took it.
*   **G9. Every run is tracked in one place.** Each ticket records questions asked,
    feedback rounds, human commits, tokens, cost where the harness reports it, elapsed
    time and whether the PR merged. One UI shows this for every Machinist run. Agents
    that developers start outside Machinist are not tracked.
*   **G10. Less system.** One control plane, one job type, three configuration places
    (factory files, control plane configuration, runner configuration). The legacy server
    path, the publisher, GitHub App onboarding and the repository maintenance type are
    removed.
*   **G11. The system works end to end.** Automated tests with a fake harness cover every
    journey below on a local runner, and the conformance suite covers a Kubernetes
    runner. One live test per harness on each runner type takes a trivial scripted issue
    through ticket, PR, reviewer comment, one follow-up and a ledger entry. These tests
    check Machinist. How well agents write code is not a release condition.

## Non-Goals

*   **Named users, roles or multi-tenancy.** One deployment serves one trusted team.
    Developers hold named tokens for attribution and revocation, with equal powers.
*   **Blocking publication or merging.** Branch protection and the login's permissions
    are the limits.
*   **Enforced triage or specification.** The agent decides whether to do them.
*   **A hosted service.** Machinist is software the team runs.
*   **Forges and trackers other than GitHub.** A developer can pass the text of a ticket
    from any tracker through MCP.
*   **Project planning, a conversational foreman, a workflow language or a step editor.**
    The playbook is the process.
*   **Replicated control planes or an external database.** A deployment has one control
    plane process.
*   **Placement rules.** Any runner with the run's harness and a free slot can take it.
*   **Resuming a live agent session.** A later run restarts from the pushed branch and
    the ticket log.

## Open Questions

The spike (step 1 of the plan) ran on 2026-11-09 with Claude Code 2.1.319 and Codex
0.157.1. [Spike results](../credentials.md) has the evidence. Q1, Q3, Q4 and Q5 are
settled. Q2 is partly settled.

*   **Q1. Codex credentials. Settled: use the fallback.** `codex app-server` ignored
    `OPENAI_API_KEY` and `CODEX_API_KEY` and sent no credentials. After
    `codex login --with-api-key` into the run's private home, it sent the key. The vendor
    documentation says app-server accepts `CODEX_ACCESS_TOKEN`. That is not tested,
    because it needs a real Business or Enterprise token.
*   **Q2. Ticket log quality. Partly settled.** A real run continued from the log and
    pushed branch, read the review and CI failure with `gh`, fixed both and kept the
    earlier commits. In that test the branch was already feature-complete, so continuing
    unfinished work is not tested. Fallback if it fails: the playbook requires a progress
    note in the PR or on the branch before each push.
*   **Q3. Provider terms. Settled.** API keys and cloud provider credentials are allowed
    on team runners, billed to the key owner. A subscription login is for its owner's own
    use, on one machine, one run at a time for Codex. See Credentials.
*   **Q4. Codex project trust. Settled: yes.** Writing the clone's real path as trusted
    in the run's private `config.toml` stops the warning. The `-c` fallback does not stop
    it, so it is dropped.
*   **Q5. Private home. Settled: no.** With a private `HOME` and `CODEX_HOME`, neither
    harness found the machine's login, on macOS. No dedicated OS user is needed to keep
    a login out of a key-based run.

## Critical User Journeys (CUJs)

### CUJ 1: Assign work from an issue

*   **Given**: Machinist is connected to a repository, and a developer has write access.
*   **When**: The developer comments `@machinist-bot please fix this` on an issue.
*   **Then**: Machinist posts one acknowledgement. The agent pushes a branch and opens
    one PR. A fresh reviewer comments on the PR. Machinist posts the PR link on the issue.

### CUJ 2: Mention by a user without write access

*   **Given**: A user without write access can comment on the issue.
*   **When**: That user mentions Machinist.
*   **Then**: No ticket is created, no agent runs and nothing is posted. The control plane
    writes a log line.

### CUJ 3: Follow up on a PR

*   **Given**: The agent opened a PR for a ticket.
*   **When**: A person requests changes, or CI fails on the PR's current commit.
*   **Then**: The same ticket continues on the same branch and PR.

### CUJ 4: CI keeps failing

*   **Given**: A ticket is running.
*   **When**: CI keeps failing until the ticket reaches its turn limit.
*   **Then**: No further run starts, the ticket becomes "needs you", and Machinist posts
    one message saying what failed.

### CUJ 5: The agent needs a decision

*   **Given**: The agent cannot continue without a choice only a person can make.
*   **When**: The agent reports a question.
*   **Then**: Machinist posts the question and the ticket becomes "needs you". A reply
    continues the same ticket. A ticket with no issue or PR shows its question in the
    Work list and MCP status.

### CUJ 6: An automation merges PRs

*   **Given**: A team adds the "review open PRs" automation file. The default branch has
    branch protection.
*   **When**: The automation runs.
*   **Then**: The agent uses `gh` to merge the PRs its prompt allows. GitHub refuses any
    merge that branch protection does not allow.

### CUJ 7: Set up with a service account

*   **Given**: An operator has a provider service account, a GitHub login for the bot, a
    control plane, and a machine or cluster for a runner.
*   **When**: The operator runs `machinist runner check --repo owner/name`.
*   **Then**: One real run clones the repository and makes a model call, on that runner
    type. The command prints the credential source the harness reported, or the error.

### CUJ 8: The control plane restarts during work

*   **Given**: A ticket is running when the control plane restarts.
*   **When**: The control plane comes back.
*   **Then**: The ticket is not duplicated. Runners keep working and reconnect, and their
    runs finish normally.

### CUJ 9: Assign work from a coding session

*   **Given**: A developer has Machinist's MCP server configured in Claude Code or Codex
    with their token, and a network route to the control plane.
*   **When**: The developer asks the session to send issue #123, or a ticket's text, to
    Machinist.
*   **Then**: The tool returns a ticket ID and link at once. The ticket follows CUJ 1.
    Closing the session does not affect the ticket.

### CUJ 10: A runner is added or lost

*   **Given**: A control plane is serving one local runner.
*   **When**: An operator starts a Kubernetes runner with a new runner token. Later, a
    runner dies during a run.
*   **Then**: The new runner takes work with no change to the control plane. Each run
    shows which runner took it. After the lost runner's lease expires, the run is
    recorded as interrupted and the ticket continues on any runner.

## Technical Plan (Architecture)

### Shape of the system

The control plane owns intake, ticket state, the ledger, the UI and MCP. It never starts
an agent. Runners do. Each runner dials out to the control plane and asks for work, so a
runner behind a firewall or on a laptop needs no inbound route.

```text
 GitHub mention --+
 Assign box ------+
 MCP tool --------+--> intake --> ticket store --> ticket loop --> run queue
 Schedule --------+                                                    ^
                                                                       |
                                  runners dial out: poll, heartbeat,   |
                                  events, complete                     |
                 +-------------------------+---------------------------+
                 |                         |                           |
            local runner              local runner            Kubernetes runner
            (Mac or VM)               (Mac or VM)             (starts gVisor Jobs)
                 |
       agent + gh in a fresh clone
                 |
                 v
       GitHub: branch, PR, comments
```

### Tickets and branches

A ticket ID is `t_` and 16 hex characters. Each ticket has one fixed branch,
`machinist/<ticket id>`, derived from its ID and not stored. Every run clones the
repository and checks out that branch if it exists on the remote, or creates it from the
default branch. The prompt names the branch.

After every agent run, the control plane finds the PR with
`gh pr list --head machinist/<ticket id> --state all`. It keeps only PRs whose head
repository is the ticket's repository, and takes the open one, or otherwise the newest.
The agent does not report a PR URL. This is how work is found again after a question, a
crash or a lost runner, and it prevents a second PR. A repository has at most one active
ticket per issue.

### The agent's report

The agent ends every run with one report. The harness enforces its shape where it can
(`--json-schema` on Claude Code):

```json
{"outcome": "pr_ready|needs_input|no_change|failed", "summary": "...", "question": "..."}
```

`no_change` means the work needed no PR, such as a question answered or an automation
that found nothing to do. The summary is the answer. A report is valid when `outcome` is
one of the four values, `summary` is not empty, and `question` is not empty for
`needs_input`.

### Ticket states

Active states are `working`, `needs_you` and `pr_ready`. Ended states are `done`, `failed`
and `cancelled`. "Message" is what Machinist posts (see Sources for where).

| State | Event | Next state | Message |
| --- | --- | --- | --- |
| (new) | Ticket created | `working` | Acknowledgement, if the ticket has an issue |
| `working` | Report `pr_ready`, PR found | `pr_ready` | None here. See the first-PR rule below |
| `working` | Report `pr_ready`, no PR found | `needs_you` | "Reported a PR but none exists for this branch" |
| `working` | Report `needs_input` | `needs_you` | The question |
| `working` | Report `no_change` | `done` | The summary |
| `working` | Report `failed` | `needs_you` | The summary |
| `working` | Missing or malformed report, or deadline passed | `needs_you` | "The run ended without a report" and the reason |
| `working` | Lease expired | `working` | None. A new run is queued |
| `working` | Turn limit reached | `needs_you` | What the last run reported |
| `working` | Input arrives during a run | `working` | None. It feeds the next run |
| `working` | PR merged | `done` | None. The run is cancelled |
| `working` | PR closed unmerged | `needs_you` | "The PR was closed. Reply to continue or cancel." The run is cancelled |
| `needs_you` | Human reply | `working` | None. The turn count restarts |
| `needs_you` | Review or CI signal | `needs_you` | None. It feeds the next run |
| `needs_you` | PR merged | `done` | None |
| `pr_ready` | A signal listed under Sources | `working`, or `needs_you` at the turn limit | As above |
| `pr_ready` | PR merged | `done` | None |
| `pr_ready` | PR closed unmerged | `needs_you` | "The PR was closed. Reply to continue or cancel" |
| Any active | Cancel | `cancelled` | "Cancelled by <name>". Any run is stopped. The PR stays open |
| Ended | Any event | No change | None |

**First PR.** The first time a PR is found for a ticket, whatever the state, Machinist
posts its link on the issue and starts the reviewers.

**Waiting input.** If a run ends with its report handled and unconsumed input is waiting,
the next run starts at once and the state returns to `working`, unless the ticket has
ended or reached its turn limit.

**Automations.** An automation ticket never waits for a person, so that one bad run cannot
stop its schedule. Any row above that leads to `needs_you` leads to `failed` instead, and
the ticket shows in the Work list. `failed` is used only for automation tickets.

A **turn** is one agent run, including interrupted ones. Reviewers do not count. A ticket
reaches its turn limit after `max_turns_without_human` runs since the last human input.
Each run has `run_timeout` as its deadline. There is no mid-run token limit, because
Kubernetes runs and Claude Code report usage only at the end. Provider spend limits cap
total cost, including subagents that Machinist cannot count.

A ticket has at most one active agent run. Reviewer runs do not count toward this.

### Reviewers

A reviewer is a prompt file. On a ticket's first PR, the engine queues one run per file
in `reviewers/`, using the ticket's harness and model. The prompt is the file, then the
repository and PR number. The clone is on the ticket's branch and gets no subagent files.
The reviewer uses `gh` to read the PR and post findings as a comment. It is a `runs` row
with `kind = reviewer`. Its report is ignored, and its success or failure changes no
ticket state. A failed reviewer shows on the ticket and is not retried, including after a
lost lease, because a retry could post twice. Comments by the bot login never count as
feedback, so a reviewer's comment starts no run. A person who agrees can reply with a
mention.

### What a run is given

The prompt is, in order: the playbook, the branch name, the report format, and the
rendered ticket log.

The log is the only state carried between runs, apart from the pushed branch. Machinist
appends the ticket text, agent reports, human comments and CI failures in order. Entries
are labelled with their author and marked as untrusted input. Rendering keeps the ticket
text and the newest entries, up to 64 KiB. The playbook tells the agent to push often,
because unpushed work is lost if a run dies.

Agent selection is one setting: `default_agent` in control plane configuration, recorded
on the ticket when it is created. An automation names its own agent.

### Data model

```text
tickets(id, source, repo, issue_number, title, state, agent, pr_number, created_by,
        human_commits, merged_at, created_at, updated_at)
ticket_entries(id, ticket_id, kind, author, body, source_key UNIQUE, created_at,
               consumed_by_run, posted_at)
runs(id, ticket_id, kind, harness, runner, status, lease_expires_at, tokens, cost, report,
     started_at, ended_at)
tokens(id, name, token_hash UNIQUE, created_at, last_used_at, revoked_at)
source_cursors(source, repo, cursor)
run_activity_events(run, sequence, body)
```

A partial unique index on `(repo, issue_number)` for active tickets enforces one active
ticket per issue. `runs.runner` is the name of the token the runner used.

A ticket's text, whether it came from a mention, the API or a schedule, is its first
entry, created in the same transaction as the ticket. `source_key` exists only on entries,
so one unique column makes all intake idempotent and a repeated comment can never create
a second ticket. Keys are `github:comment:<id>`, `github:review:<id>`,
`github:ci:<head sha>`, `api:<request_id>` (Assign box and MCP) and
`schedule:<name>:<occurrence>`. `source` is `github`, `api` or `schedule:<name>`.

The new database is a new file. Old databases stay on disk unopened and are not imported.

### Sources

Polling is used because the control plane has a private address, which webhooks cannot
reach.

*   **GitHub comments.** Once a minute per repository, `gh api` lists issue comments since
    a stored cursor. This list includes PR conversation comments. On first poll the cursor
    is set to now, so old mentions are not processed. The cursor reads back 5 minutes
    each time, and source keys remove repeats. Issue and PR bodies are not read for
    mentions.
*   **Who counts.** A comment counts only if its author is not the bot login and has
    write, maintain or admin permission, using one GitHub permission check.
*   **Routing a comment.** A comment on an issue or PR belonging to an active ticket
    becomes input if it mentions the bot, or if the ticket is `needs_you`. Other comments
    are ignored. A mention on an issue with no active ticket creates a ticket. A mention
    on a PR with no ticket is logged and ignored.
*   **PR signals.** For each ticket with a PR, once a minute: PR state, reviews requesting
    changes by permitted authors, and one CI failure per commit once all checks on it have
    finished. The log entry only points at the signal, such as "CI failed on abc123". The
    agent reads the detail with `gh`. Human pushes to the branch are allowed and counted.
*   **Assign box and MCP.** Both call the same intake with a caller-supplied `request_id`.
    A request for an issue with an active ticket returns that ticket.
*   **Schedule.** Automation files use `internal/triggers` cron. The next occurrence comes
    from the automation's newest ticket. Missed occurrences collapse into one, and no
    ticket starts while the automation's last one is active.

**Outbound messages** are posted only by the control plane, from `ticket_entries` rows
with `posted_at` empty, on the issue, or on the PR if there is no issue. A crash between
posting and recording can post a message twice. Tickets with no issue or PR show messages
in the Work list and MCP status only.

The control plane's login must be a dedicated bot account. Otherwise that person's own
mentions are ignored as the bot's.

### Runners

A runner is a `machinist runner start` process holding a runner token. It polls only when
it has a free slot, and the number of slots is the concurrency limit. Four HTTP routes
connect it to the control plane:

```text
POST /api/v1/runners/poll         {harnesses} -> one run, or 204
POST /api/v1/runs/{id}/heartbeat  {} -> {cancel: bool}, every 30 seconds
POST /api/v1/runs/{id}/events     {events[]}
POST /api/v1/runs/{id}/complete   {state, report, tokens, cost}
```

These routes exist today under similar names, and their payloads shrink to this. A run
carries: run ID, ticket ID, repository, branch, prompt, the agent's subagent files,
harness, model, deadline and the bot's Git name and email. It carries no secrets. Run IDs
keep today's format (`run_` and 24 hex characters). The Git name is the configured
`handle`, and the email is `<handle>@users.noreply.github.com`.

The control plane gives the oldest queued run to the first polling runner offering its
harness, and records the runner's name and last poll in memory for the Runners page. Every
runner serves every connected repository. Control plane and runners are released together
and should be upgraded together.

**Leases.** A lease lasts 10 minutes and is stored in the database, so runs outlive a
control plane restart. Each heartbeat renews it. When a lease expires, the run is marked
interrupted and the ticket queues a new run with a new ID. Calls for a run that is no
longer active get 409, and the runner then stops that agent. A runner that cannot reach
the control plane keeps working and retries. If its lease expires meanwhile, or a runner
crashes, an old agent can overlap with the new run until its deadline. Git then rejects
the push from whichever falls behind. On a local runner, systemd kills the process group
of a runner that crashes.

Inside a runner, one interface hides where the agent runs:

```text
Execute(ctx, RunRequest) -> RunResult
RunRequest: run ID, ticket, repository, branch, prompt, agent files, harness, model,
            deadline, Git name and email
RunResult:  report, tokens, cost, events
```

Cancelling `ctx` stops the run.

Each run starts with a fresh clone. The runner writes the agent's subagents into the
clone's `.claude/agents` and `.codex/agents` and lists them in `.git/info/exclude`. The
harness finds them through normal project-level discovery.

*   **Local runner.** The agent is a process on that machine, with a private `HOME` and
    `CODEX_HOME` for each run and an environment built from an allowlist. It runs as the
    runner's OS user, which should be a dedicated account.
*   **Kubernetes runner.** The runner is a process in the cluster that starts one gVisor
    Job per run through the existing connector, using the existing egress proxy.
    Activity appears when the run ends.

Both launch the same fixed commands, which are not configurable:

```text
claude --print --verbose --output-format stream-json --include-partial-messages
       --dangerously-skip-permissions --json-schema <report schema> --model <model>
codex app-server --stdio -c features.multi_agent=true -c sandbox_mode="danger-full-access"
```

The Codex model is sent in `thread/start`. These flags turn off each harness's own
permission checks and sandbox. The limits on an agent are its OS user or gVisor Job, its
GitHub login and, on Kubernetes, the egress proxy. Each Codex run writes its clone's real
path (symlinks resolved) as trusted in its private `config.toml` before starting Codex
(Q4). Without that entry Codex trusts the folder itself but still sends the
`configWarning` that fails the run.

### Credentials

Credentials live on runners. A credential is the list of environment variable names that
the harness already reads, passed through unchanged from the runner's own environment. So
keys, service accounts, federation and cloud providers need no per-kind code, and path
variables or provider switches work as they are. A runner offers only the harnesses it
has a credential for.

On a local runner, the agent gets those variables and `GH_TOKEN`, and nothing else from
the runner's environment except an allowlist (`PATH`, `LANG`, `TMPDIR`, proxy and CA
settings). On a Kubernetes runner, each Job gets one Secret through `envFrom`, which must
hold those variables and `GH_TOKEN`. The runner process never reads it.

Codex is the one exception to passing variables through, because `codex app-server` does
not read a key from its environment (Q1). When a Codex credential holds `OPENAI_API_KEY`,
the runner or Job pipes it to `codex login --with-api-key` into the run's private
`CODEX_HOME`, and the run directory is deleted when the run ends.

A local runner can set `login = true` on a credential instead. It then uses the OS user's
existing harness login and normal home directory. Set `slots = 1` for a Codex login.
Kubernetes runners have no `login` option and no login command. A Claude
`CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` is an ordinary variable, so it works
in the Secret, but it is only for the owner's own use (Q3). Teams use API keys or a cloud
provider.

### Configuration

Control plane, `config.toml`. It reads its GitHub token from `GH_TOKEN`:

```toml
[server]
listen = "10.0.0.5:7331"
database = "~/.machinist/server/tickets.db"
factory = "~/.machinist/factory"
default_agent = "default"
max_turns_without_human = 5
run_timeout = "60m"
# tls_cert, tls_key

[github]
handle = "machinist-bot"
repositories = ["owner/name"]
```

Runner, `runner.toml`:

```toml
control_plane = "http://10.0.0.5:7331"
token_file = "~/.machinist/runner.token"
data_directory = "~/.machinist/runner"
slots = 2

[credentials.claude]
env = ["ANTHROPIC_API_KEY"]

# [credentials.codex]
# login = true

# [kubernetes]   makes this a Kubernetes runner
# context, namespace, image, proxy_url, secret, allowed_hosts
```

Old keys fail at startup and name the key.

Every `GH_TOKEN` should be fine-grained, limited to connected repositories, without admin
rights, and belong to the bot login.

### Factory files

```text
factory/
  agents/<agent>/
    agent.toml              harness, model
    playbook.md             instructions for the agent
    .claude/agents/*.md     Claude Code subagents, in its own format
    .codex/agents/*.toml    Codex custom agents, in its own format
  reviewers/<name>.md       reviewer prompts
  automations/<name>.md     TOML front matter: schedule, timezone, repo, agent. Body is the prompt
```

The factory lives on the control plane and is read at startup, and an invalid factory
stops startup. A change needs a restart. On Kubernetes it is a mounted volume. Machinist
does not read, translate or route subagents. Starter subagents have `machinist-` names so
that they do not replace a repository's own. The UI shows these files and can trigger an
automation. An automation covers one repository, and the merge template uses its own
`maintainer` agent, because the default playbook forbids merging.

### Ledger

Most figures are computed from `runs` and `ticket_entries` when read:

*   **Questions:** `needs_input` reports.
*   **Feedback rounds:** agent runs started by a human comment or review.
*   **Human commits:** when the PR merges, commits on the branch whose author email is not
    the bot's. Stored on the ticket.

The Work page shows each as an average per merged PR, for tickets that did not come from
a schedule. Tokens come from the harness's result. Cost is recorded where the harness
reports it (`total_cost_usd` on Claude Code) and is otherwise blank.

### Access

The control plane serves the UI, the API and MCP at `/mcp` on one address, which the
operator keeps on a private network. TLS is optional. Every route needs a token, except
`GET /healthz`, `POST /api/v1/session` and static assets. Tokens are random and stored as
hashes. There is one kind of token, used by developers and runners alike.

Signing in to the UI stores the token in an HttpOnly, SameSite=Strict cookie, marked
Secure under TLS. Requiring a token on every route also closes issue #463 (Host header
checks). Revoking a token ends its sign-in.

Developer routes, all JSON. Errors are `{"error": "..."}` with 400, 401, 404 or 409.

| Route | Body or query | Returns |
| --- | --- | --- |
| `POST /api/v1/session`, `DELETE` | `{token}` | Sets or clears the cookie |
| `POST /api/v1/tickets` | `{request_id, repo, issue_number}` or `{request_id, repo, title, body}` | Ticket. 200 if it already existed |
| `GET /api/v1/tickets` | `state`, `repo` | Newest 200 tickets |
| `GET /api/v1/tickets/{id}` | | Ticket, entries, runs |
| `GET /api/v1/runs/{id}/events` | `after` | Activity page, in today's format |
| `POST /api/v1/tickets/{id}/answer` | `{text}` | Ticket. 409 unless `needs_you` |
| `POST /api/v1/tickets/{id}/cancel` | | Ticket. 409 if ended |
| `GET /api/v1/ledger` | `repo`, `since` | The G9 figures |
| `GET /api/v1/runners` | | Each runner's name, harnesses and last contact |
| `GET /api/v1/automations` | | Each automation, next due time and last ticket |
| `POST /api/v1/automations/{name}/run` | | Ticket |

MCP tools mirror these: `assign_ticket(request_id, repo, issue_number | title+body)`,
`get_ticket(id)`, `list_tickets(state, repo)`, `answer_ticket(id, text)` and
`cancel_ticket(id)`. Each returns the ticket: ID, state, link, PR, and any waiting question.

`machinist token create <name>`, `list` and `revoke` open the database directly. That is
how the first token is made, and tokens are managed only there.

### Interface

Navigation is Assign, Work, Automations and Runners. Assign has one box and a repository
selector. Work lists tickets by what each waits on, answers questions, and shows which
runner took each run. Automations lists automation files with next due time, last ticket
and a run button. Runners lists each runner and when it last made contact.

## Alternatives Considered

### Alternative A: Fixed or configurable stages

*   **Pros**: Triage and review cannot be skipped. Each stage is visible.
*   **Cons**: Routing stays in Go, and each stage needs a handoff format.
*   **Reason for Rejection**: Rejected by author: this design also restarts from a log
    after feedback, so it does lose reasoning across runs. What it removes is the routing
    and per-stage handoff code, and one agent with subagents keeps context inside a run.

### Alternative B: Stages as messages to one live session

*   **Pros**: Enforced order with no context loss.
*   **Cons**: Kubernetes Jobs end, so restarts need a second path.
*   **Reason for Rejection**: Rejected by author: two execution paths for one behavior.

### Alternative C: Machinist stops at a pushed branch

*   **Pros**: Small scope, with a clear contract. This is the approach of
    [Warren](https://github.com/jayminwest/warren).
*   **Cons**: Leaves follow-up and measurement to each team.
*   **Reason for Rejection**: Rejected by author: without PR follow-up and a ledger, a
    team still supervises each agent and has no shared record.

### Alternative D: Agents without GitHub access, and a separate publisher

*   **Pros**: An agent cannot push or merge.
*   **Cons**: Needs a publisher, Git bundle exchange, a store of candidate changes,
    bot-identity onboarding, and another way for automations to read GitHub.
*   **Reason for Rejection**: Rejected by author: it costs those five components, while
    branch protection already limits what a login can merge.

### Alternative E: A required review status, or no reviewer

*   **Pros**: A required status makes "cannot merge" enforceable by GitHub. No reviewer
    removes a concept.
*   **Cons**: A status needs return-loop code. With no reviewer, review rests only on the
    agent's own subagents, which share its context.
*   **Reason for Rejection**: Rejected by author: one advisory comment gives an
    independent opinion for one extra run and no state.

### Alternative F: Build on GitHub Actions

*   **Pros**: Runners, schedules, secrets and logs already exist, and self-hosted runners
    can use a team's own machines. Mention-triggered agents on Actions are the smallest
    option, and `examples/github-actions/` shows one.
*   **Cons**: No ticket across runs, no ledger in agent terms, and no MCP assignment or
    question handling.
*   **Reason for Rejection**: Rejected by author: the reason to build this is one record
    of agent work across all runners, in terms of questions, feedback rounds and accepted
    work. Plain job running alone would not justify it.

### Alternative G: Buy Warp Factories

*   **Pros**: Nothing to build or run.
*   **Cons**: Work and credentials move to another product's model, and teams keep less
    control over where agents run. Cost was not evaluated.
*   **Reason for Rejection**: Rejected by author: Machinist runs on the team's machines
    with the team's existing harness credentials.

### Alternative H: MCP that only posts a GitHub comment

*   **Pros**: No new network path or token store.
*   **Cons**: Every ticket becomes a public-to-the-repository issue, and pickup waits for
    the next poll.
*   **Reason for Rejection**: Rejected by author: a tokened endpoint costs one route and
    allows tickets with no issue.

### Alternative I: Roles on tokens

*   **Pros**: Developers could be limited to assigning and reading work.
*   **Cons**: Adds a role model, and any token holder can already instruct an agent that
    holds the credentials, so roles would protect little.
*   **Reason for Rejection**: Rejected by author: one trust level is what the system
    actually provides.

### Alternative J: One server that starts agents itself

*   **Pros**: No network protocol, leases or runner tokens. One program to deploy.
*   **Cons**: Agents run only where the server runs. A team using both Kubernetes and
    local machines needs two servers, two databases and two UIs.
*   **Reason for Rejection**: Rejected by author: there is one control plane, and any
    number of runners.

### Alternative K: Process sandbox or Docker on a local runner

*   **Pros**: Stronger file and network isolation.
*   **Cons**: Adds a dependency on each machine and differs between macOS and Linux.
*   **Reason for Rejection**: Rejected by author: every agent already holds a GitHub
    login, so teams that need a boundary use Kubernetes runners.

## Non-Functional Requirements

### Security and privacy

*   **Agent authority.** Every agent can do whatever the GitHub login allows. Issue text,
    PR comments and repository files can be written by outsiders, and that text can steer
    an agent. The limits are the login's permissions and branch protection. On a
    repository that accepts outside PRs, automatic merging requires branch protection
    with human review.
*   **Who can trigger a run.** A mention requires write access. This limits who starts
    work, not what an issue says.
*   **Credentials in runs.** The agent can read its model credential and GitHub token.
    Use dedicated service accounts with narrow scopes and provider spend limits. An
    expired GitHub token shows as a failed poll in the control plane log.
*   **One token, many repositories.** A prompt injection in one repository can reach any
    other repository the token covers. Run one deployment per trust domain.
*   **Tokens.** Any token holder can read run history, claim runs, and assign work that
    tells an agent to reveal its credentials. Treat a token as equal to the GitHub login
    and model credential. Give each person and runner their own, so it can be revoked.
    Tokens are sent in the clear unless the private network or TLS protects them, and
    nothing stops an operator exposing the address publicly.
*   **Local runners.** Concurrent runs under one OS user can read each other's files and
    environments, and the agent has open network access. If a runner shares an OS user
    with the control plane, an agent can read and change the control plane's database.
*   **Kubernetes runners.** The egress proxy blocks arbitrary hosts. It does not stop data
    leaving through allowed services, such as a push to another repository on
    `github.com`.

### Limits

*   One control plane process and one SQLite file. If it is down, runners finish their
    current runs and no new work starts.
*   One GitHub token per control plane for polling. Polling costs one list request per
    repository per minute, plus one PR request per open ticket, and all runs on a runner
    share that runner's token. This suits tens of repositories and open PRs.
*   Concurrency is the sum of runner slots. A clone is removed after its run. A runner
    needs disk for its largest repository, times its slots.
*   Mid-run spend is not limited by Machinist.

### Rollout and rollback

*   This is a breaking change. Old job modes and configuration keys fail at startup and
    name the key. Runs in progress at upgrade are not carried over. Operators keep or
    delete old databases themselves.
*   Delivery is on one long-lived branch cut from `main`. It merges when G11 passes.
*   Before merge, rollback is staying on `main`. After release, rollback is the previous
    release tag with its old database.

## Additional Resources

*   [Warren](https://github.com/jayminwest/warren)
*   [How Warp Factories work](https://docs.warp.dev/factories/how-factories-work/)
*   [DeepSeek super-agent philosophy](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/000-philosophy.md),
    the source of "trust the agent and let it route"
*   [Claude API authentication](https://platform.claude.com/docs/en/manage-claude/authentication)
*   [Claude Code authentication](https://code.claude.com/docs/en/authentication)
*   [Claude Code subagents](https://code.claude.com/docs/en/sub-agents)
*   [Codex authentication](https://learn.chatgpt.com/docs/auth)
*   [Codex service accounts](https://learn.chatgpt.com/docs/enterprise/service-accounts)
*   [Codex custom agents](https://learn.chatgpt.com/docs/agent-configuration/subagents)

## Future Improvements

*   **Runner labels:** route a run to particular runners.
*   **Enforced triage:** a run before the agent, if the ledger shows it is needed.
*   **Required review status:** let branch protection require the reviewer's result.
*   **Reviewer on every push:** review later commits as well as the first PR.
*   **Revert tracking:** record when a merged PR is later reverted.
*   **Live activity on Kubernetes:** stream events during the run.
*   **More sources:** Linear, Jira and GitLab.
*   **Stronger local isolation:** Docker or a process sandbox.
*   **Spec intake:** designs are written outside Machinist, for example with EGM, and
    arrive as a job. A later version could accept a spec file as a ticket's request.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Work starts from `main` on one branch, in small PRs into that branch. The uncommitted
changes on `demo/run-event-search` are not carried over. New tests use a fake harness and
a fake `gh` executable unless marked live.

### Order of work

Legacy code is deleted first, so old and new code never have to coexist. Every step ends
with `go build ./... && go test ./...` passing, except step 1, which changes no product
code.

1.  **Spike.** Settle Q1 to Q5 with a fixture repository. Record results in
    `docs/credentials.md`. Exit: every question has an answer or its fallback chosen.
2.  **Delete.** Remove the rows under Deleted files. Add `internal/protocol/report.go` and
    `internal/config/runner.go`. Make the `internal/runner`, `internal/protocol` and
    `internal/config` changes listed below, and rewrite `examples/config.toml`.
    `examples/assets.go` embeds only `config.toml` for now, and `initialFiles` in
    `internal/cli/init.go` matches it. `internal/kuberunner` loses only the fields that
    name deleted types, because its rewrite is step 7. `internal/cli` keeps `init`,
    `update`, `version`, `kubernetes` and `sandbox`, with `kubernetes` reading runner
    configuration. Exit: build and tests pass with no server.
3.  **Local run path.** `internal/runner` run setup, ticket store, factory loader, runner
    routes, local runner, engine, `internal/controlplane/server.go`, `machinist start`,
    `machinist runner`. Exit: a ticket created through the store runs to a report with a
    fake harness.
4.  **GitHub.** Sources, PR lookup, follow-ups, reviewers, outbound messages, ledger.
    Exit: CUJs 1 to 5 pass with fake `gh`.
5.  **Access and MCP.** Tokens, auth, ticket routes, MCP. Exit: CUJ 9 passes.
6.  **Automations and UI.** Schedule source, the four pages and the final embed. Exit: CUJ
    6 and `npm test` pass.
7.  **Kubernetes runner.** Exit: the conformance suite passes on both runner types.
8.  **Documentation and live tests.** Exit: G11 passes.

Tracked files that are not listed below do not change.

### New files

| File | Purpose |
| --- | --- |
| `internal/tickets/store.go`, `store_test.go` | Tables, transitions from the state table, idempotent intake, one active ticket per issue, run queue and leases. Event ingest moved from `controlplane/run_events.go` |
| `internal/tickets/log.go`, `log_test.go` | Render entries into the prompt: order, 64 KiB limit, untrusted labels |
| `internal/tickets/ledger.go`, `ledger_test.go` | The Ledger figures, human commit count at merge |
| `internal/engine/engine.go`, `engine_test.go` | The ticket loop: every row of the state table, turn limit, reviewers once, CUJs 1, 3, 4, 5 |
| `internal/engine/leases.go`, `leases_test.go` | Expire leases and requeue, CUJ 8 and CUJ 10 |
| `internal/engine/outbox.go`, `outbox_test.go` | Post unposted messages |
| `internal/sources/github.go`, `github_test.go` | Comment poller, routing, permission check, first-poll cursor, CUJ 2 |
| `internal/sources/followups.go`, `followups_test.go` | PR state, reviews and CI signals, adapted from `github_followups.go` without the head-change block or author-association trust |
| `internal/sources/schedule.go`, `schedule_test.go` | Cron occurrences to tickets; coalescing moved from `factory_automations.go` |
| `internal/factory/factory.go`, `factory_test.go` | Load and validate the factory directory |
| `internal/ghclient/client.go`, `client_test.go` | Moved from `github_cli.go`, `github_journal.go` and `github_followups.go`, with their tests: no-shell execution, error classification, redaction, permission, comments, PR lookup, branch protection flag |
| `internal/mcpserver/server.go`, `server_test.go` | Five MCP tools on `github.com/modelcontextprotocol/go-sdk`, CUJ 9 |
| `internal/protocol/report.go`, `report_test.go` | Report type and validation |
| `internal/agentrunner/daemon.go`, `daemon_test.go` | Poll, heartbeat, stop on lost lease, complete. Adapted from `managedworker/worker.go` |
| `internal/agentrunner/client.go`, `events.go`, `events_test.go`, `progress.go` | Moved from `managedworker/client.go`, `run_events.go`, `run_events_test.go`, `progress.go`. The client drops its refusal of `http://` for non-loopback hosts, and lease fields go |
| `internal/agentrunner/execute.go` | The `Execute` interface |
| `internal/agentrunner/local.go`, `local_test.go` | Private home, allowlisted environment, login mode, process start and stop |
| `internal/agentrunner/kubernetes.go`, `kubernetes_test.go` | Adapter over `kuberunner.Connector`, tested with a fake `kubectl` |
| `internal/agentrunner/check.go`, `check_test.go` | `runner check`: one `Execute` with a probe prompt, on either runner type, then print the reported credential source. CUJ 7 |
| `internal/agentrunner/conformance_test.go` | One suite for both runner types: run, report, cancel, timeout |
| `internal/runner/request.go` | `RunRequest` and `RunResult`, here so that both `agentrunner` and `kuberunner` can use them |
| `internal/runner/workspace.go`, `workspace_test.go` | New code shared by both runner types: fresh clone, branch checkout or creation, write and exclude subagents, Git identity, Codex trust entry, `codex login --with-api-key` into the private home when the credential holds a key, GitHub environment through `githubauth` |
| `internal/runner/harness.go`, `harness_test.go` | The two fixed commands |
| `internal/e2e/live_test.go` | G11 live scenario under `MACHINIST_NATIVE_LIVE`, importing the engine and both runner types: each harness on each runner type |
| `internal/config/runner.go`, `runner_test.go` | Runner configuration, replacing `Worker`; old keys rejected |
| `internal/cli/token.go`, `token_test.go` | `machinist token create`, `list`, `revoke` |
| `internal/cli/runner.go`, `runner_test.go` | `machinist runner start`, `check` |
| `internal/controlplane/server.go`, `server_test.go` | New server: routes, static UI, startup of sources and engine, branch protection warnings. `/healthz` returns the version |
| `internal/controlplane/auth.go`, `auth_test.go` | Token middleware and cookie; every route needs a token |
| `internal/controlplane/tickets_http.go`, `tickets_http_test.go` | Developer routes for tickets, answer, cancel, ledger, automations |
| `internal/controlplane/runners_http.go`, `runners_http_test.go` | The four runner routes and the runner list |
| `internal/controlplane/web/src/runners.jsx`, `runners.test.js` | Runners page |
| `internal/controlplane/web/src/assign.jsx`, `assign.test.js` | Assign page |
| `internal/controlplane/web/src/work.jsx`, `work.test.js` | Work page: grouping, answers, ledger figures |
| `internal/controlplane/web/src/sign-in.jsx` | Token sign-in |
| `examples/runner.toml` | Commented runner configuration |
| `examples/factory/agents/default/agent.toml`, `playbook.md` | Default agent: triage, build, review with subagents, push often, open a PR, never merge, return the report |
| `examples/factory/agents/default/.claude/agents/machinist-triage.md`, `machinist-implementer.md`, `machinist-reviewer.md` | Claude Code subagents, from the software-factory plugin's roles |
| `examples/factory/agents/default/.codex/agents/machinist-triage.toml`, `machinist-implementer.toml`, `machinist-reviewer.toml` | Codex agents, same roles |
| `examples/factory/agents/maintainer/agent.toml`, `playbook.md` | Agent allowed to merge, for the automation template |
| `examples/factory/reviewers/review.md` | Default reviewer |
| `examples/factory/automations/review-open-prs.md` | Merge automation |
| `deploy/kubernetes/runner-daemon/resources.yaml`, `kustomization.yaml` | Runner Deployment using the control plane image. Holds the Job Role moved from the control plane, with `get` on the one agent Secret |
| `deploy/systemd/machinist-runner.service` | Local runner with `KillMode=control-group` |
| `docs/factory.md`, `docs/credentials.md`, `docs/runners.md`, `docs/mcp.md` | Factory layout, credentials and spike results, adding runners, connecting MCP |

### Modified files

| File | Change and reason |
| --- | --- |
| `go.mod`, `go.sum` | Add the MCP SDK at its latest release. Remove nothing else, because `internal/tickets` keeps `modernc.org/sqlite` |
| `cmd/machinist/main_test.go` | Replace the `worker run` binary tests with tests of `init` and `version` |
| `internal/cli/root.go`, `root_test.go` | Commands become `init`, `start`, `runner`, `token`, `update`, `kubernetes`, `sandbox`, `version`. Remove `run`, `submit` and `worker` |
| `internal/cli/init.go` | Write both configuration files, copy the starter factory, create a runner token named `local` |
| `internal/cli/kubernetes.go` | Remove `login`; `install` reads runner configuration; `sandbox receive` and `run` follow `kuberunner` |
| `internal/config/config.go`, `config_test.go` | Keep `[server]` and `[github]` above. Remove `Worker`, `state`, worker token, concurrency, trigger and workflow settings. Keep the command type `internal/runner` needs |
| `internal/config/kubernetes.go` | Replace `AuthSecret` with `Secret`; add `AllowedHosts` |
| `internal/protocol/protocol.go` | Shrink to the runner routes' payloads |
| `internal/githubauth/environment.go` | Remove App wording. The helper is otherwise reused, called from `internal/runner/workspace.go` |
| `internal/kuberunner/connector.go`, `connector_test.go` | Remove the ConfigMap lock, `retainAuth`, `Login` and `loginClaude`, the `agent` cluster setting (the run request names the harness), Git bundle input and output, and `readOnly`. `ready` checks the one Secret. `manifest` adds `envFrom` and removes the `codex-auth` volume. A test now requires `GH_TOKEN` in the Job |
| `internal/kuberunner/sandbox.go` | `Request` becomes `runner.RunRequest`, with subagent files inside. `Receive` accepts only `request.json`. `Prepare` probes the network and creates `/source/repository`. `Sandbox` calls `runner` workspace setup, then launches through `runner.Execute` with the fixed commands. Remove `ValidateAuth`, `Head`, `Dirty`, the `forced_login_method` setting and the `/credentials` read |
| `internal/kuberunner/install.go` | Keep `api.anthropic.com`. Add `api.github.com`, `uploads.github.com` and configured `AllowedHosts` to the proxy list |
| `internal/kuberunner/network.go` | `ProbeNetwork` always probes `github.com` and loses its `provider` argument |
| `internal/runner/runner.go`, `internal/runner` tests | `Options` gains `Prompt`, and `Environment` becomes the complete environment. `Result` gains `Report` and `Cost` and loses `StepResult`. Parse `total_cost_usd` from Claude Code's result event. Remove `Factory`, `Revision`, `Task`, `PrepareInputs`, `Workflow`, `ArtifactKey`, `ReviewEvidence` and `mergeEnvironment` |
| `internal/runner/native.go`, `native_test.go` | Remove `nativeEnvironment` and the `--bare` rejection. Remove both checks that require a subscription login, keeping the reported source for `runner check`. Claude Code structured output is always on, so `nativeCommand` loses `structuredResult` and its check for a configured `--json-schema` |
| `internal/runner/native_result.go` | `nativeResultSchema` and parsing use the report |
| `internal/runner/native_live_test.go` | Run the fixed commands with key credentials, proving subagent loading from the clone |
| `internal/controlplane/web/src/main.jsx`, `routes.js`, `routes.test.js`, `navigation.test.js` | Four pages and sign-in |
| `internal/controlplane/web/src/automations.jsx`, `automations.test.js`, `automation-outcome.js`, `automation-outcome.test.js` | Read-only list with run button; read ticket outcomes |
| `internal/controlplane/web/src/task-detail.jsx`, `task-display.jsx`, `task-presentation.js`, `task-presentation.test.js` | Ticket detail and the six states |
| `internal/controlplane/web/src/status-loader.js`, `status-ui.test.js` | Load tickets with the cookie |
| `internal/controlplane/web/src/native-activity.js`, `native-activity.jsx`, `native-activity.test.js`, `run-activity.test.js` | Read the activity stream by run |
| `internal/controlplane/web/src/run-metrics.js`, `run-metrics.test.js` | Ledger figures |
| `internal/controlplane/web/src/styles.css`, `visual-system.test.js` | Support the new pages |
| `internal/controlplane/web/dist/*` | Rebuilt bundle |
| `examples/README.md`, `examples/config.toml` | New configuration and starter factory |
| `examples/assets.go` | `//go:embed config.toml runner.toml all:factory`. `all:` is needed for the dot directories, and a test checks they are copied |
| `deploy/kubernetes/deploy.py`, `test_deploy.py` | Replace `/api/v1/status` and `/api/v1/onboarding/check` with `/healthz`, use `tickets.db`, remove `[server.kubernetes]`, and roll out the runner Deployment with the control plane |
| `deploy/kubernetes/cd-resources.yaml` | Let the deployer patch the runner Deployment |
| `deploy/kubernetes/control-plane/resources.yaml` | Add a Service, and an ingress rule from runner pods. `/healthz` probes, `GH_TOKEN`, mounted factory. Remove the Job Role |
| `deploy/kubernetes/control-plane-gke/kustomization.yaml` | Include the runner Deployment |
| `deploy/kubernetes/runner.Dockerfile` | Add Claude Code and `gh` |
| `scripts/setup-vm.sh` | Install the runner service in place of the worker service |
| `skills/machinist/SKILL.md` | Assign work through MCP |
| `README.md`, `ARCHITECTURE.md`, `SECURITY.md`, `.github/site/index.html` | The product, architecture and security model in this design |
| `docs/README.md`, `docs/configuration.md`, `docs/development.md`, `docs/native-agents.md`, `docs/vm-deployment.md`, `docs/local-factory/continuous-deployment.md` | Replace old commands and configuration with this design's |

### Deleted files

| Files | Reason |
| --- | --- |
| `agent.py`, `evals/README.md`, `evals/__init__.py`, `evals/test_agent.py` | Separate orchestrator |
| All 7 files in `internal/runner/prompts/` | Replaced by the playbook. Their embeds go with `workflow.go` |
| `internal/runner/revision.go`, `revision_test.go`, `workflow.go`, `workflow_test.go` | Role prompts and results |
| `internal/artifacts/store.go`, `store_test.go` | Artifact workflows |
| `internal/cli/workflows_test.go` | Workflows |
| `internal/config/storage.go`, `artifacts_test.go`, `triggers.go`, `triggers_test.go`, `workflows.go`, `workflows_test.go` | Old configuration |
| `internal/protocol/artifacts.go`, `factory.go`, `factory_test.go`, `revision.go`, `workflow.go` | Role results and artifacts. `protocol.go` shrinks and `run_events.go` stays |
| All non-web files in `internal/controlplane/`, including `server.go` | Old server paths, recipes, journal, labels, onboarding, projects and maintenance. A new `server.go` arrives in step 3, and the parts reused are moved as listed above |
| `internal/managedworker/` | Moved to `internal/agentrunner` as listed above. The rest (artifacts, evidence, GitHub credentials, publication, workspace, candidate publishing) is removed |
| `internal/controlplane/web/src/`: `analytics-state.js`, `analytics.jsx`, `artifacts.jsx`, `triggers.jsx`, `trigger-state.js`, `trigger-state.test.js`, `runs-board.js`, `runs-board.test.js`, `stage-progress.jsx`, `stage-progress.test.js`, `dashboard-state.js`, `dashboard.test.js`, `workflows.test.js`, `cleanup-retry.test.js`, `project-composer.test.js`, `runs-view.test.js`, `work-queue.test.js`, `worker-status.test.js`, `onboarding.jsx`, `onboarding.test.js`, `projects.jsx`, `projects.test.js`, `catalog.jsx`, `catalog.test.js`, `catalog-copy.test.js` | Pages for concepts that no longer exist |
| `examples/worker.toml`, `examples/workflow_scripts_test.go`, `examples/github-actions/README.md`, `examples/github-actions/machinist-comment-intake.yml`, all files in `examples/prompts/` and `examples/workflows/` | Replaced by the starter factory |
| `deploy/systemd/machinist-worker.service`, `scripts/setup-neo-worker.sh` | Replaced by the runner service |
| `docs/agent-workflow.md`, `docs/artifacts.md`, `docs/coding-workflows-in-code.md`, `docs/kubernetes-agent-delivery.md`, `docs/local-agent-delivery.md`, `docs/task-artifact-model.md`, `docs/task-guide.md`, `docs/workflows.md`, `docs/local-factory/README.md`, `automations.md`, `github-followups.md`, `kubernetes.md`, `cloud-control-plane.md`, `local-jobs.md` | Describe removed behavior. `docs/workflow-roadmap.md`, `docs/local-factory/spec.md` and `warp-evidence.md` stay as history |

### Verification Plan

*   **Build**: `go build ./...` and, in `internal/controlplane/web`, `npm run build`.
*   **Unit tests**: `go test ./...` and `npm test`.
*   **Static analysis**: `go vet ./...`, and `gofmt -l .` prints nothing.
*   **Coverage requirement**: every CUJ has an automated test, and every row of the state
    table is covered in `internal/engine`.
*   **Integration**: `internal/agentrunner/conformance_test.go` passes for both runner
    types.
*   **Live**: `MACHINIST_NATIVE_LIVE=1 go test ./internal/runner ./internal/e2e -v
    -count=1` passes. In a disposable repository, a ticket goes from mention to merged for
    each harness on each runner type.

--------------------------------------------------------------------------------

*Drafted with EGM*
