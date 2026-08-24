# Connector-driven software factory

> **Status:** Proposed for review

## 1. Executive summary

Factory currently has strong worker safety and recovery machinery, but its
product model has accumulated several overlapping ways to describe prompts,
automation, and execution. A developer must understand Tasks, Procedures,
Runs, Work, Sessions, Executions, Attempts, Pipelines, Workers, and older
Workflow and Automation concepts before they can answer the basic question:
what work will the agents do, and what happened?

This design replaces that product model with one config-driven path. A
Connector receives a work item from GitHub, and later Linear, or the operator
submits it with `factory run`. Factory freezes the input and a selected
Pipeline. Each Pipeline stage selects an Agent profile with its own prompt,
runtime, model, repository access policy, timeout, and output schema. Workers
execute the stages and the browser monitors the resulting Run. The built-in
Pipeline plans, builds, and verifies the change, with at most two review-driven
revision cycles before it either creates a verified draft pull request or asks
for human attention.

The main downside is a deliberate compatibility break. Existing Factory data
remains readable only by the existing binary. The new server uses a fresh
database and does not translate active Work. The execution safety core is
preserved, but the stored product model and public API are replaced.

PR #355 should not be merged as the new foundation. Its prompt and Pipeline
ideas should be carried into this design, then the pull request should close
after this replacement is accepted.

## 2. Context and scope

The [current architecture](../../ARCHITECTURE.md) already provides SQLite
state, worker heartbeats, leased attempts, process-group supervision, runtime
event adapters, isolated Git worktrees, cancellation, retained failures, and
checkpoint recovery. Those parts solve real operational problems and remain
useful.

The current authoring and execution model is insufficient for the intended
product. Pipeline stages contain only a name and prompt. Runtime, timeout, and
outcome settings apply to the enclosing Task. A later stage does not receive a
typed result from an earlier stage. Stage handoff depends on files left in a
shared worktree, and the default Pipeline is still a single general agent.
There is no current Connector contract that makes GitHub labels, polling,
Linear delegation, and manual runs feed the same admission path.

This proposal incorporates the useful inputs without copying their product
shape wholesale:

- [pull request #355](https://github.com/owainlewis/factory/pull/355) confirms
  the need for prompt Pipelines, but the reset makes stage Agents, typed
  handoffs, and source admission first-class instead of extending the current
  model again;
- [Foreman](https://ask-foreman.dev/) informs the default plan, build, verify
  prompts and the use of explicit review feedback, while Factory keeps its own
  Worker safety and durable stage records;
- the [Eve software factory template](https://github.com/vercel-labs/eve-software-factory-template/tree/main)
  supports treating the issue queue as input and configuration as the durable
  product surface, while this design adds a provider-neutral Connector boundary
  and exact checkpoint recovery.

Once this design ships, configuration files define Connectors, Agent profiles,
and Pipelines. The browser displays effective configuration and operational
state but does not edit the configuration. One Run represents one work item in
one repository. A batch command creates several independent Runs. Pipeline
fan-out, arbitrary graphs, and multi-repository Runs are not part of this
design.

This design covers:

- one released `factory` executable with server, worker, and client commands;
- manual and GitHub issue admission through one durable inbox;
- a built-in GitHub polling Connector;
- versioned, config-defined Pipelines and per-stage Agent profiles;
- a built-in plan, build, verify Pipeline with a bounded revision loop;
- structured handoff artifacts and exact Git checkpoints;
- local and remote Workers using the existing runtime adapters;
- a monitoring-first browser UI;
- a clean database transition from the current developer preview.

Linear is represented in the Connector boundary but is not implemented in the
first release of this design. Its Agent Session lifecycle, identity, and reply
model should be added only after the GitHub path proves the Connector contract.

## 3. System context

```text
GitHub issue polling                  factory run
              |                          |
              +------------+-------------+
                           v
                       Connector
                 verify, dedupe, snapshot
                           |
                           v
                         Run
             frozen input and config bundle
                           |
                           v
                     Stage scheduler
                  plan -> build -> verify
                           |
                           v
                        Worker
          worktree, runtime, lease, process group
                           |
             +-------------+-------------+
             v                           v
     structured artifacts         published Git SHA
             |                           |
             +-------------+-------------+
                           v
                  draft pull request

Browser and CLI read Run, Stage, Attempt, event, artifact, and Worker views
from the same server API.
```

The server owns Connectors, admission, config snapshots, Run state, scheduling,
leases, artifacts, delivery, SQLite, the API, and the embedded UI. A Worker
owns repository preparation, worktrees, runtime processes, local supervision,
and cleanup. A configured coding-agent runtime owns model interaction and code
changes inside the access granted to its stage. GitHub owns issues, branches,
pull requests, and merge state.

## 4. Proposed design

### How it works

A maintainer applies the configured `factory` label to GitHub issue `#412`.
The GitHub Connector observes the label on its next poll. It verifies the
repository, issue state, label event, and actor permission. It stores a
Connector Event with a unique observation key before doing further work.
Repeated polling or process restart finds the same source lineage and cannot
create another root Run. Removing and re-adding the label, renaming the
Connector, or manually submitting the same issue returns the existing Run. A
deliberate repeat uses `factory run --rebuild`, which creates a new Run linked
to its predecessor.

The Connector fetches a bounded issue snapshot containing the stable
repository and issue identities, URL, title, body, selected comments, labels,
the triggering actor, and provider revision data. Provider text is marked as
untrusted. The config loader resolves the selected Pipeline, every referenced
prompt and schema, and every Agent profile. It creates one canonical config
bundle and stores its complete bytes, bundle hash, source revision when known,
and dirty status. Admission creates one Run with the immutable issue, base
commit, Pipeline, and config snapshots. It then queues the first logical Stage
Run, `plan[1]`.

An eligible Worker claims a new Stage Attempt with a random lease. It checks out
the exact base commit in a clean worktree and starts the planner Agent. The
planner has read-only repository access and writes only its final structured
result through the Factory result contract. Factory validates the result
against the frozen plan schema and stores it as an artifact. A valid result
completes `plan[1]` and creates `build[1]`.

The builder receives the frozen work-item snapshot and plan artifact.
`build[1]` edits a worktree based on the Run's exact base commit. A later build
iteration starts from the previous successful mutating checkpoint and also
receives the previous build report and verifier findings. The runtime edits,
checks, and commits locally. Factory does not inject a GitHub or Git push
credential into it.

After the runtime exits, the Worker validates the structured result and clean
worktree, then records a publish intent containing the Attempt ID, expected
remote SHA, proposed head SHA, and validated result. Only then does the Worker
use its own credential to compare-and-swap the exact Run branch. It verifies
the remote head and records publication complete. Build success requires this
durable checkpoint before Factory creates `verify[1]`.

The verifier uses a fresh checkout of the exact published SHA. It receives the
original input, plan, build report, diff metadata, and acceptance criteria. It
does not receive the builder's reasoning. Its result must judge every criterion
and return `approve`, `request_changes`, or `reject`.

An approval causes the delivery service to create or update one draft pull
request and post the final link to the issue. A request for changes creates
`build[2]` from the exact `build[1]` checkpoint, carrying the prior build report
and verification findings, then `verify[2]`. Each later revision preserves all
previous successful changes. The default permits two revision cycles. A
rejection or exhausted revision limit leaves the Run needing human attention
and posts the remaining findings. Factory never marks its own pull request
ready and never merges.

The operator sees the Connector acknowledgment on GitHub immediately after
admission. The browser shows the Run, active stage, selected Agent, Worker,
attempt history, logs, artifacts, checkpoints, revision count, and final pull
request. `factory run` follows the same path but creates a manual Connector
Event with a durable request key.

### Prompt contract and shipped default

`factory init` creates a complete `.factory/` starter bundle and refuses to
overwrite an existing path. The bundle includes the three Agent profiles,
Pipeline, schemas, and editable `plan.md`, `build.md`, and `verify.md` prompts.
When given a GitHub repository and host credential name, it also creates a
manual Source profile. A developer can then prove the loop without enabling a
polling Connector by running `factory run owner/repository#123`.

Prompt assembly is deterministic and contains these ordered sections:

1. Factory's versioned runtime and result-envelope contract;
2. the trusted stage prompt from the frozen config bundle;
3. the frozen work-item snapshot, clearly marked untrusted;
4. named predecessor artifacts and exact Git checkpoint metadata;
5. the stage's output schema and active limits.

Templates may reference only documented scalar display fields. They cannot
inject files, executable names, arguments, tools, credentials, or raw JSON.
`factory render` emits this exact assembly with secrets redacted, so prompt
changes can be reviewed before admission.

The shipped plan prompt requires repository inspection, an ordered approach,
explicit acceptance criteria, tests, risks, assumptions, and one essential
clarification when work cannot safely proceed. It forbids repository changes.
The build prompt requires the smallest complete change, relevant checks, a
clean local commit, and an evidence-backed report. A revision build must retain
the prior successful change and address each verifier finding. The verify
prompt requires independent inspection of the exact commit and a result for
every acceptance criterion. It forbids code changes and may approve only when
all blocking criteria pass.

### Components and responsibilities

#### Unified command

The `factory` executable owns command dispatch. `factory web` runs the server
and embedded UI. `factory worker` runs a Worker. Finite commands are bounded API
clients. The command does not open SQLite for finite operations and `factory
run` never starts an agent process.

The release does not contain `factory-server` or `factory-worker` compatibility
binaries. Process roles remain separate even though they use one executable.

#### Config bundle loader

The loader owns parsing, reference resolution, schema validation, canonical
encoding, and hashing for `.factory/config.toml` and its prompt and schema
files. It supplies an immutable bundle to admission. It does not resolve
secrets, choose a Worker, or reread configuration during a Run.

#### Provider adapters and Source profiles

A Source profile is frozen configuration, not another durable lifecycle
resource. It selects a compiled provider adapter, repository allowlist, and
host-owned credential name. Manual admission uses it to resolve a reference,
fetch stable identities and the base SHA, and report the final outcome.
Connectors reference the same profile for polling and delivery. This prevents
manual runs from depending on an enabled Connector while keeping one provider
contract.

#### Connector service

The Connector service owns provider authentication, trigger evaluation,
authorization, polling cursors, observation identity, source-lineage claims,
input snapshots, acknowledgments, progress projection, and final source links.
It depends on provider-specific adapters and the Run admission service. It does
not execute Pipelines or give provider credentials to coding agents.

The first adapter is GitHub. Connector is a typed Go interface with built-in
implementations, not a dynamic plugin system in this release.

#### Run service

The Run service owns immutable admission, Run state, stage transitions,
revision bounds, cancellation, explicit retry, artifact references, Git
checkpoints, and final outcome. It depends on the Connector Event and frozen
config bundle. It does not own processes or mutable provider state.

#### Stage scheduler

The scheduler owns pending Stage Run ordering, Worker capability matching, and
creation of leased Stage Attempts. It prefers a healthy Worker that already has
the repository and prior Run checkout, but correctness never depends on Worker
affinity. It does not interpret model prose or decide review verdicts.

#### Worker and runtime adapters

The Worker owns runtime capability probes, repository caches, exact-checkout
preparation, worktrees, process groups, leases, event forwarding, result
capture, Git postconditions, and cleanup or retention. Runtime adapters own
command arguments and event parsing for Codex, Claude Code, and Pi. Workers do
not load Pipeline configuration from the target worktree and do not write
Run state without an active lease.

#### Artifact store

The artifact store owns validated stage results and bounded supporting files.
Every artifact records content type, schema version, content hash, producer
Stage Attempt, and size. It does not store Git repositories or replace the
published branch checkpoint.

#### Delivery service

The delivery service owns idempotent draft pull-request creation and final
Connector reporting. It uses provider credentials held by the server. It does
not implement, verify, mark ready, or merge code.

#### Browser UI

The browser owns monitoring views for Connectors, Runs, Stages, Attempts,
Workers, artifacts, and attention requests. It may cancel, explicitly retry,
and resume a Run after human input. It shows effective configuration but does
not edit Connectors, Agents, Pipelines, prompts, or schemas.

### Decisions

#### Preserve the execution core and replace the product layer

The lease transaction, Worker heartbeat, process-group supervisor, runtime
event adapters, worktree identity checks, cancellation, retained failures, and
checkpoint cleanup rules remain. The Task, Procedure, Work, Session, Execution,
and current Pipeline database model, API resources, and matching UI are
replaced by Connector, Run, Stage Run, and Stage Attempt. Reusing safety code
reduces operational risk; preserving the current product schema would keep the
confusing model that this reset is meant to remove.

#### One Run processes one work item in one repository

A batch creates several independent Runs. This avoids bringing back a hidden
target object only to support aggregate history. The cost is that a batch has
no durable parent in the first release; the CLI can print all admitted Run IDs.

#### Pipelines are linear with one bounded feedback edge

Stages run in declared order. A stage may be named as the verifier for one
bounded revision loop back to a prior mutating stage. Arbitrary branches, joins,
parallel stages, and DAGs are rejected. This is enough for plan, build, verify
without making Factory a general workflow engine.

#### Logical Stage Runs and process Stage Attempts are separate

`build[2]` is a new semantic revision. A retry after a process crash is another
Attempt under the same `build[2]`. This preserves review history and prevents a
stale process from overwriting a later result. The cost is one internal Attempt
record beneath the simpler user-facing Stage model.

A Stage Run is identified by `(stage_id, revision_index, resume_index)` and is
immutable after a terminal outcome. Initial stages have `resume_index = 0`.
A schema-valid `needs_input` outcome is terminal for that Stage Run and may be
the predecessor only of a same-stage successor with `resume_index + 1`.
Infrastructure retries remain new Attempts under the current Stage Run; review
feedback increments revision instead of resume.

#### Handoffs are artifacts and Git commits

Structured plans and reports are stored as artifacts. Mutating stages also
publish an exact Git SHA through a Worker-owned postflight. Later stages never
depend on an uncommitted worktree, one machine, or a repository-root handoff
file. This adds a push at each mutating boundary but makes recovery and
independent review reliable.

Publication uses a durable intent before the push. If a Worker disappears
after pushing but before completing the Attempt, that Attempt becomes `lost`
and remains immutable. The next Worker claims a new leased reconciliation
Attempt under the same Stage Run and does not start an agent process. It
compares the remote branch with the stored expected and proposed SHAs. A
proposed match lets the reconciliation Attempt adopt the stored validated
result and checkpoint. An expected match records that publication did not
happen and permits a normal runtime Attempt from the prior checkpoint only when
runtime budget remains; otherwise the Run needs human attention. Any other head
enters `publish-uncertain` without overwriting the branch.

Runtime and reconciliation Attempts have separate counters. A reconciliation
Attempt never consumes a runtime retry. Factory permits at most two
reconciliation Attempts for one publish intent; exhausting them leaves the Run
needing human attention.

#### Configuration is authoritative and the UI is not an editor

Config as code provides review, reuse, prompt consistency, and exact history.
The browser displays the frozen bundle used by each Run. It cannot create a
second mutable source of Pipeline truth. The cost is that configuration changes
use an editor and `factory validate` rather than a browser form.

#### Repository access policy is not called a security sandbox

The configured coding runtime is trusted software running as the Worker user.
`read-only` means the adapter uses its supported read-only mode and the Worker
proves that the repository and published refs did not change. It protects the
repository contract but does not claim hostile-process isolation from the host,
network, or all user credentials. A future container backend may provide that
stronger boundary.

#### GitHub ships first and Linear follows the same contract later

GitHub polling provides the first real Connector. Linear Agent Sessions have a
different identity and activity lifecycle and remain in developer preview.
Implementing Linear and GitHub webhooks only after the polling path is useful
reduces the chance that the Connector interface merely hides unfinished
provider designs.

#### Human review remains the shipping gate

Factory's unattended ceiling is a verified draft pull request. Marking ready
and merging remain human actions. This limits the consequence of a mistaken
agent verdict and keeps delivery reversible.

#### A source item has one Run lineage

Every Connector has an explicit, immutable config ID and a mutable display
name. Observation identity uses that stable Connector ID plus provider event
evidence. Admission separately claims a source lineage keyed by provider,
stable repository ID, item kind, and stable item ID. Polling, manual admission,
label churn, Connector rename, removal, or replacement cannot create a second
root Run for the same source item. A repeat must be explicit through `factory
run --rebuild`, which appends a predecessor-linked Run to that lineage.

Removing a Connector tombstones its cursor and observations but never releases
source-lineage claims. Restoring the same config ID resumes its cursor. A new
Connector ID begins a new observation stream but still sees the existing source
lineage. This makes naming changes safe at the cost of keeping tombstones.

#### The new data model uses a fresh database

The existing developer-preview database is not migrated in place. A current
server must drain or cancel active Work and write a backup before the new server
starts. The new default is `~/.factory/v2/factory.sqlite3`; the old database and
retained worktrees remain untouched. The cost is that historical Runs are not
visible in the new UI.

### Delivery order

1. Introduce the new schema and frozen config bundle with `factory init`,
   `validate`, and `render`. Prove canonical hashes and strict diagnostics.
2. Add Run, Stage Run, Stage Attempt, artifact, result-envelope, resume, and
   publication state using fake runtimes and local Git fixtures.
3. Ship manual `factory run` with the default plan, build, verify loop across
   the existing Codex, Claude Code, and Pi adapters.
4. Add the GitHub polling Connector, source-lineage deduplication, issue
   progress, and idempotent draft pull-request delivery.
5. Replace the current browser with the monitoring UI, run the scratch-repo
   end-to-end suite, then remove the superseded product API and commands.

Each slice must leave its new path runnable and tested. The old product path is
not removed until slice five passes. No compatibility adapter writes both data
models.

## 5. Invariants and requirements

### Invariants

- `INV-1`: One Run names exactly one repository, one source item, and one frozen Pipeline bundle.
- `INV-2`: A source-lineage key creates at most one root Run.
- `INV-3`: In-flight Runs never reread mutable config, prompts, schemas, or source-item text.
- `INV-4`: One Stage Run has one stage key, semantic revision, resume index, and immutable predecessor set.
- `INV-5`: Every process start creates a distinct Stage Attempt with its own lease and event stream.
- `INV-6`: Only the holder of an unexpired lease can mutate an active Stage Attempt.
- `INV-7`: A stale or repeated completion cannot replace a stored terminal Attempt result.
- `INV-8`: A Stage Run succeeds only after its result validates against the frozen result schema.
- `INV-9`: A mutating Stage Run succeeds only after the expected remote branch resolves to the recorded head SHA.
- `INV-10`: A later pipeline stage reads only successful predecessors; a resume Stage Run reads only its validated `needs_input` predecessor and durable checkpoint.
- `INV-11`: Review-driven revision and infrastructure retry never share an identity or counter.
- `INV-12`: The configured revision limit cannot be exceeded by retry, restart, or replay.
- `INV-13`: Provider text and repository content are untrusted context; config prompts are trusted operator instruction.
- `INV-14`: Factory never injects Connector, operator, Git, Worker, or lease credentials into coding-agent processes, prompts, artifacts, or worktrees.
- `INV-15`: Cancellation prevents any later Attempt result from advancing the Run.
- `INV-16`: Factory never derives a structured verdict by searching arbitrary prose.
- `INV-17`: Factory never marks a pull request ready and exposes no merge capability.
- `INV-18`: Cleanup removes a worktree only after its identity, ownership, process state, and durable checkpoint are proved.
- `INV-19`: Existing Factory databases and retained worktrees are never modified by the new server.
- `INV-20`: Finite CLI commands use the server API and never open its SQLite database.
- `INV-21`: Every revision of a mutating stage starts from the prior successful mutating checkpoint.
- `INV-22`: Connector display names and observation streams cannot change the source-lineage identity.
- `INV-23`: Factory provides no Git push credential to runtime subprocesses; configured publication is performed only by Worker postflight for the exact Run ref.
- `INV-24`: An uncertain publication is reconciled from stored expected and proposed SHAs before retry or completion.
- `INV-25`: Human-input resume is distinct from infrastructure retry and review revision and has its own bounded counter.
- `INV-26`: Reconciliation Attempts never consume runtime Attempt budget and cannot exceed their own frozen bound.

### Requirements

- Configuration supports one through twenty stages per Pipeline and zero or one bounded revision loop.
- `factory init` creates a complete default bundle, refuses overwrite, and produces a bundle that passes `factory validate` unchanged.
- Every stage selects an Agent profile with runtime, optional model and effort, repository access policy, timeout, result schema, and prompt.
- `factory validate` rejects missing files, unknown fields, unsupported variables, invalid schemas, broken stage references, unbounded loops, and unsupported Agent settings.
- `factory render` displays the exact trusted prompt sections, untrusted context sections, artifact references, Agent profile, and bundle hash without admitting a Run.
- Manual and Connector admission use the same request validation and Run creation transaction.
- GitHub polling, manual admission, and config reload share source-lineage identity and cannot create duplicate root Runs.
- A disabled Connector admits no new Runs but does not stop existing Runs.
- Connector IDs are explicit and immutable. Renaming changes only display data; removal tombstones state and never releases source claims.
- A config reload is explicit and atomic. Existing Runs keep their bundle; new Runs use the new bundle.
- The scheduler orders compatible pending Stage Runs by creation time. Worker affinity may break ties only.
- A Run waiting for Worker capacity displays the missing capability and time spent waiting.
- Plan and verify use fresh agent processes. Verify uses a fresh checkout of the exact build SHA.
- An unattended clarification posts the exact question to the source and pauses the Run in `needs-input` only after any mutating work has a durable checkpoint.
- Only an authenticated loopback operator may answer in the first release. The first answer for an idempotency key wins; an identical replay returns the same result and a different replay conflicts.
- All operator mutations require the owner-only operator credential. Browser mutations also require same-origin and CSRF checks.
- Resuming stores the human answer as trusted context and creates a successor Stage Run with the same stage ID and semantic revision and an incremented resume index.
- Human-input resume is limited to three successor Stage Runs per stage and semantic revision. It does not consume infrastructure retry or review-revision limits.
- A Stage Run permits at most two runtime Attempts and at most two reconciliation Attempts per publish intent. The counters are independent.
- The UI displays source, Pipeline and bundle hash, current stage, revision, Agent, Worker, latest progress, Attempts, artifacts, checkpoint, failure, and pull-request evidence.

## 6. Interfaces and data

### CLI

```text
factory web [--config PATH] [--listen ADDRESS] [--database PATH]
factory worker [--config PATH]

factory init [--directory PATH] [--github OWNER/REPOSITORY] [--credential NAME]
factory run [--source NAME] [--pipeline NAME] [--request-key KEY] [--rebuild] [--wait] REFERENCE
factory validate [--config PATH]
factory render [--config PATH] [--pipeline NAME] REFERENCE

factory runs [--state STATE] [--cursor CURSOR]
factory show RUN_ID
factory logs [--follow] RUN_ID [--stage STAGE_KEY] [--attempt N]
factory cancel RUN_ID
factory retry RUN_ID [--stage STAGE_KEY]
factory answer RUN_ID [--request-key KEY] --message TEXT
factory connectors
factory credentials set NAME [--from-stdin]
factory credentials check NAME
factory workers
factory version
```

`REFERENCE` accepts a GitHub issue URL, `owner/repository#number`, or a future
Connector-qualified reference. `factory run` requires the server to be healthy.
It uses `--source` when supplied. Otherwise, exactly one Source profile must
match the referenced repository; zero or multiple matches return an actionable
error before admission. The Source profile fetches stable provider identities,
the issue snapshot, actor evidence where applicable, and the exact base SHA.
When no request key is supplied, the CLI durably journals a generated key until
it receives and flushes an authoritative admission response. The same rule
applies to `factory answer` until it receives the authoritative resume response.

### Configuration

The default config path is `.factory/config.toml` in the server's selected
configuration root. Referenced files resolve below `.factory/`; absolute paths,
symlinks escaping the root, duplicate keys, and unknown fields are rejected.
Secrets are referenced by host-owned credential name and never appear in the
frozen bundle. A Source profile supplies provider identity, repository scope,
and credential selection to both manual admission and Connectors.

```toml
version = 1
default_pipeline = "change"

[sources.github-main]
type = "github"
credential = "github-main"
repositories = ["owainlewis/factory"]

[[connectors]]
id = "github-factory-issues"
name = "Factory issues"
type = "github"
source = "github-main"
pipeline = "change"
enabled = true

[connectors.github]
repository = "owainlewis/factory"
label = "factory"
poll_interval = "30s"

[agents.planner]
runtime = "codex"
model = "gpt-5.6"
effort = "high"
repository_access = "read-only"
timeout = "30m"

[agents.builder]
runtime = "claude-code"
repository_access = "write"
timeout = "2h"

[agents.verifier]
runtime = "codex"
model = "gpt-5.6"
effort = "high"
repository_access = "read-only"
timeout = "30m"

[[pipelines.change.stages]]
id = "plan"
agent = "planner"
prompt = "prompts/plan.md"
schema = "schemas/plan.json"

[[pipelines.change.stages]]
id = "build"
agent = "builder"
prompt = "prompts/build.md"
schema = "schemas/build.json"
inputs = ["plan"]

[[pipelines.change.stages]]
id = "verify"
agent = "verifier"
prompt = "prompts/verify.md"
schema = "schemas/verify.json"
inputs = ["plan", "build"]

[pipelines.change.revision_loop]
review_stage = "verify"
revision_stage = "build"
on = "request_changes"
max_revisions = 2
```

Credential names resolve in the server's owner-only credential store and are
managed with `factory credentials`. Secret values are never written to the
repository, frozen bundle, database event stream, logs, or browser. A missing
credential does not invalidate the bundle, but its Source is unavailable and
manual or polling admission returns the exact missing name before fetching the
work item.

### Connector Event

A Connector Event stores:

- Source profile ID, optional Connector ID, and config-bundle hash;
- admission kind and optional stable provider event ID;
- observation key and receive time;
- repository stable ID and canonical identity;
- source-item stable ID, revision, type, and URL;
- triggering actor identity and verified permission;
- bounded normalized snapshot and optional bounded raw payload hash;
- processing state, Run ID, failure, and retry time.

For GitHub issue intake, the observation key is derived from the immutable
config Connector ID and the stable provider timeline event. Admission then
claims the global source lineage derived from provider, stable repository ID,
item kind, and stable item ID. Manual admission canonicalizes a GitHub reference
to the same lineage and stores its request key as the observation key with no
Connector ID. An explicit rebuild uses a new request key, records the prior Run
as its predecessor, and is rejected while that prior Run is nonterminal.

### Stage result envelope

Every Agent returns one versioned envelope:

```json
{
  "version": 1,
  "status": "succeeded",
  "summary": "Implemented and verified the requested change.",
  "output": {},
  "artifacts": []
}
```

`status` is `succeeded`, `needs_input`, or `failed`. A successful result must
validate `output` against the stage schema. A `needs_input` result must contain
one `question` object with nonempty `text` and `reason`; it may contain a
schema-valid partial output. A `failed` result must contain a stable error code
and message and is a semantic failure, not an automatic infrastructure retry.
Factory owns these outer fields. Runtime adapters use native structured output
when available and otherwise strictly parse the final response. Raw final
output is stored separately for diagnosis. A missing or malformed envelope
fails the Attempt and is never treated as a semantic result.

The Agent never supplies the durable Git checkpoint. For a mutating result,
the Worker appends the server-verified branch, base SHA, and head SHA after the
publish postflight. For mutating `needs_input`, the runtime must leave a clean
local commit and the Worker must publish and store it before pausing. Plan and
verify may pause without a Git checkpoint because their input SHA is immutable.

An accepted answer creates a successor Stage Run with the same stage ID and
semantic revision and `resume_index + 1`. Its predecessor is the terminal
`needs_input` Stage Run. Its trusted inputs are the exact question, answer,
prior schema-valid output, artifacts, and last durable checkpoint. The answer
request key is unique per pause: an identical replay returns the same successor
and a different answer with the same key returns conflict. Server restart does
not change this transition.

The built-in schemas contain:

- Plan: problem, approach, ordered steps, affected surface, risks, acceptance criteria, test strategy, assumptions, and open questions.
- Build: base and head SHA, change summary, verification commands and results, deviations, and known limitations.
- Verify: verdict, one evidence-backed result per acceptance criterion, blocking findings, suggestions, and summary.

### HTTP API

The initial API exposes bounded resources under `/api/v1`:

```text
POST /runs
GET  /connectors
POST /connectors/{id}/check

GET  /runs
GET  /runs/{id}
POST /runs/{id}/cancel
POST /runs/{id}/retry
POST /runs/{id}/answer

PUT  /credentials/{name}
POST /credentials/{name}/check

GET  /runs/{id}/stages/{stage_run_id}/attempts/{attempt_id}/events
GET  /artifacts/{id}

PUT  /workers/{id}
PUT  /workers/{id}/heartbeat
POST /workers/{id}/claims
POST /stage-attempts/{id}/start
PUT  /stage-attempts/{id}/heartbeat
POST /stage-attempts/{id}/events
POST /stage-attempts/{id}/publish-intent
POST /stage-attempts/{id}/publish-complete
POST /stage-attempts/{id}/complete
```

The local operator API remains loopback-only and requires a random operator
credential for every mutation. The server creates it in the new state directory
with owner-only permissions. The CLI reads it from that path. The browser
exchanges it on a login page for an `HttpOnly`, `SameSite=Strict` session and
uses a per-session CSRF token; mutation routes reject absent credentials and
foreign origins. Local processes running as the same OS identity remain inside
the trusted-local boundary. Remote Worker routes use the existing authenticated
TLS boundary. Collection responses are paginated and bounded.

### Stored data

The new database contains these durable groups:

- config bundles and referenced file snapshots;
- Connectors, cursors, and Connector Events;
- Runs and immutable source snapshots;
- Stage Runs and Stage Attempts;
- Attempt events and validated artifacts;
- Workers, capabilities, credentials, and heartbeats;
- delivery operations and provider evidence.

Lease fields belong to Stage Attempts. Pipeline and Agent definitions are
stored only as frozen config-bundle snapshots, not mutable database authoring
records.

### Naming and identity

Factory IDs are UUIDv7 values generated by the server. A failed ID generation
aborts the transaction. User-facing Pipeline, stage, Agent, Source, and
Connector IDs are lowercase ASCII identifiers matching
`[a-z][a-z0-9-]{0,62}` and are unique within their config namespace. A
Connector also has a mutable display name. Its ID cannot change across reloads;
replacing it is an explicit remove and add operation.

Repository identity uses the provider's stable repository ID plus the canonical
current name. A rename updates the display name on later Connector Events but
does not change existing Run identity. Source-item identity uses provider,
repository stable ID, item kind, and provider item ID. URLs and issue numbers
are display and lookup fields, not durable identity.

The Run branch is `factory/run-<lowercase-run-id>`. Every Worker publication
compares the expected predecessor SHA and may update only that exact ref. An
unexpected remote head enters `publish-uncertain`, retains the worktree, and
requires inspection. It is never automatically overwritten.

## 7. Failure behavior and lifecycle

An invalid config prevents `factory web` from starting before it opens a
listener or begins Connector polling. `factory validate` reports every
independent config error in stable file and field order. An explicit reload
validates a complete candidate bundle first, then atomically activates it. A
failed reload leaves the prior bundle active.

Polling runs every configured interval, defaults to 30 seconds, and uses
exponential backoff from 30 seconds to 15 minutes after provider failures. A
successful check resets the backoff. Provider rate-limit responses pause until
the provider reset time. Connector failure is visible but does not stop Workers
or existing Runs.

Repeated polling, later label events, manual admission, and Connector rename
return the existing Run lineage. A provider event that cannot prove its
triggering actor fails closed and creates no Run. A source snapshot that
exceeds its bound is retained as a failed
Connector Event with a source link and size diagnostic.

When no Worker matches a stage, the Stage Run remains queued with the exact
missing runtime or repository-access capability. Compatible stages are claimed
oldest first. No maximum wait can be promised without capacity, so the UI shows
age and the blocking capability rather than claiming eventual execution.

A Stage Attempt holds a 30-second lease and renews it every 10 seconds. Worker
or network loss prevents renewal, stops the owned process group, and marks the
Attempt lost after lease expiry. Factory automatically retries one lost or
malformed-output Attempt by default. A second failure stops the Stage Run and
requires an explicit retry. Starting another Attempt never deletes prior logs
or results.

A planner or verifier that changes the repository fails its Attempt. A builder
that exits without a clean worktree and valid local commit fails and retains
its worktree. After a publish intent exists, recovery compares the remote Run
branch with the stored SHAs before it starts another process. The original
Attempt remains terminal as `lost`; a new leased reconciliation Attempt owns
the transition. If the remote is the proposed head, that Attempt verifies and
adopts the stored result. If it is the expected predecessor, it records no
publication and Factory may create a normal runtime Attempt from that
checkpoint when runtime budget remains. Exhausted runtime budget enters human
attention. Any other head enters `publish-uncertain` and blocks automatic work.
A verification
`request_changes` result is a successful Stage Run and advances the semantic
revision counter. It is not an infrastructure retry.

A valid `needs_input` result makes the current Stage Run terminal without
succeeding it and pauses the Run. A mutating Stage Run follows the same publish
protocol before it may pause. An authenticated answer with a new request key
creates one successor Stage Run from the exact stored inputs and checkpoint.
Duplicate answers are handled idempotently, and a fourth resume for the same
stage and semantic revision is rejected into human attention. Server or Worker
restart does not convert a resume into an infrastructure retry or a review
revision.

An external draft-PR request is idempotent by Run branch and a hidden Run marker
in the PR body. A timeout or lost response is retried with the same key. If the
branch already has the expected PR, Factory records it instead of opening
another. Delivery failure leaves an approved Run in `delivery-failed`; it does
not rerun agents.

Cancellation marks the Run cancelling, stops future claims, requests stop on
the active Attempt, and becomes terminal only after the process is stopped or
the lease expires. A completion arriving after cancellation is recorded on the
Attempt but cannot advance the Run. Explicit retry creates a new Stage Attempt
or semantic Stage Run as selected by the failed condition; it never overwrites
history.

Disabling a Connector stops new evaluation after the current provider request
finishes. Existing Runs continue. Disabling or removing an Agent or Pipeline in
new config affects only later admissions. Removing a Worker stops new claims;
active Attempts follow their leases.

On shutdown, the server stops Connector scheduling and new claims first, gives
HTTP requests 10 seconds to finish, then closes SQLite. Workers stop polling,
cancel active process groups, flush bounded events, and retain uncertain
worktrees. Startup sweeps expired leases before admitting or claiming work.

The new server refuses to open a current-format Factory database. It prints the
required backup and new-database path. It never starts with a partly converted
schema.

## 8. Security, privacy, and operations

Connector identity is established from a host-owned provider credential.
GitHub label activation also verifies that the label actor has at least the
configured repository role, defaulting to triage. Authorization happens before
Run creation. Polling must fetch the matching timeline event and cannot infer
trust from current label state alone.

Issue bodies, comments, provider metadata, agent artifacts, and repository
content are untrusted. Prompt assembly places trusted stage instructions,
untrusted source context, and model-produced artifacts in separate bounded
sections. Template variables cannot select tools, executables, credentials,
paths, or runtime arguments.

Agent profiles select only compiled runtime adapters and supported settings.
Repository configuration cannot provide an arbitrary executable or argument
list. A Worker starts the runtime with an isolated minimal home and an
environment allowlist containing only the selected runtime's model credential
and documented nonsecret values. Factory, Connector, Git, Worker, and lease
credentials are not injected. The runtime may commit in its local worktree.

After the child process has stopped, Worker postflight obtains a scoped Git
credential from host-owned storage and may compare-and-swap only the exact Run
branch recorded in the publish intent. The credential is never inherited by a
child, placed in a prompt or artifact, written to the worktree, or reused for a
different ref. The server separately owns Connector and pull-request delivery
credentials.

The local Worker threat model trusts the configured runtime executable as the
Worker user. Repository `read-only` is an enforced repository postcondition,
not hostile-process containment. Environment removal and a minimal home do not
prevent a same-user process from seeking credentials elsewhere on the host.
The UI and documentation must state that the runtime may retain the Worker
user's ambient filesystem and network authority. Operators who require strong
credential separation must run the Worker under a dedicated OS identity with
no access to server or Git credential storage. A future isolated Worker backend
may automate that stronger boundary.

Config files and the database may contain prompts, issue text, comments,
results, and logs. They use owner-only files and directories. Connector secrets
remain outside config bundles and are never returned by the API. Artifact
downloads set restrictive content types and cannot execute active HTML in the
Factory origin.

Shared limits for the first release are:

- 200 Connectors, 200 Pipelines, 200 Agent profiles, and 20 stages per Pipeline;
- 1 MiB canonical config bundle;
- 256 KiB normalized source snapshot;
- 128 KiB assembled stage prompt;
- 256 KiB structured Stage result;
- 1 MiB per artifact and 10 MiB of artifacts per Run;
- 10 MiB of events per Stage Attempt;
- 100 queued or active Runs per Connector;
- one through 100 Worker slots, with 10 as the default;
- two automatic runtime Attempts per Stage Run;
- two reconciliation Attempts per publish intent;
- two review-driven revisions in the built-in Pipeline, configurable from zero through five.

Admission or upload fails before mutation when a static size limit is exceeded.
Event ingestion truncates only after storing an explicit truncation marker and
continues process supervision. A Connector at its active-Run limit pauses new
admission and displays the limit; it does not discard source events.

## 9. Acceptance criteria

- `AC-1`: One released `factory` binary runs web, worker, and finite client commands without compatibility executables.
- `AC-2`: A valid GitHub label event creates one Run that freezes the issue snapshot, base SHA, and complete config bundle.
- `AC-3`: Repeated polling, label churn, and server restart return the same Run lineage for one source item.
- `AC-4`: A label event from an actor below the configured permission creates no Run and records an actionable Connector failure.
- `AC-5`: `factory run` and GitHub admission produce the same stored Run and Stage shapes.
- `AC-6`: Each stage uses its configured runtime, model settings, repository access, timeout, prompt, and schema.
- `AC-7`: The built-in Pipeline creates plan, build, and verify stages in order with fresh agent processes.
- `AC-8`: The plan result is schema-valid, stored as an artifact, and supplied to build without a repository-root handoff file.
- `AC-9`: Build cannot succeed until its branch and exact remote head SHA are verified.
- `AC-10`: Verify uses a fresh checkout of the exact build SHA and reports evidence for every acceptance criterion.
- `AC-11`: A request-changes verdict creates a new build and verify iteration without changing the infrastructure-attempt counter.
- `AC-12`: Revision processing stops at the frozen maximum and leaves the Run needing human attention.
- `AC-13`: Worker loss creates a separate Attempt, rejects stale completion, and can continue from stored artifacts and the published SHA on another Worker.
- `AC-14`: A malformed structured result fails the Attempt and cannot advance the Pipeline.
- `AC-15`: An approved Run creates at most one draft pull request and reports it to the source item.
- `AC-16`: No Factory API or agent tool can mark a pull request ready or merge it.
- `AC-17`: The browser displays current stage, iteration, Agent, Worker, Attempts, logs, artifacts, checkpoint, attention reason, and final PR.
- `AC-18`: The browser cannot mutate Connector, Agent, Pipeline, prompt, or schema configuration.
- `AC-19`: A successful config reload affects new Runs only; a failed reload leaves the previous bundle active.
- `AC-20`: Cancelling a Run prevents any later Attempt completion from advancing it.
- `AC-21`: The new server rejects the legacy database without modifying it or its retained worktrees.
- `AC-22`: Disabled Connectors admit no new Runs while their existing Runs continue.
- `AC-23`: `build[2]` starts from the successful `build[1]` SHA and preserves its changes while applying verifier findings.
- `AC-24`: Worker loss after publish intent leaves the original Attempt `lost`. A new leased reconciliation Attempt adopts a proposed remote SHA without rerunning the agent, permits a normal runtime retry when the remote remains at the expected predecessor and runtime budget remains, enters human attention when that budget is exhausted, and enters `publish-uncertain` without overwrite for an unrelated head. Reconciliation does not consume runtime Attempt budget.
- `AC-25`: Connector rename, removal and restoration, config reload, polling, and manual submission cannot create a second root Run for the same GitHub issue; `--rebuild` creates a predecessor-linked successor.
- `AC-26`: A planner `needs_input` result survives server restart, accepts one idempotent operator answer, and resumes the same semantic revision with an incremented resume index.
- `AC-27`: A builder `needs_input` result publishes its partial commit before pausing and resumes from that exact checkpoint after restart.
- `AC-28`: Factory injects no Git or GitHub credential into a runtime child, and Worker postflight rejects publication to any ref other than the Attempt's exact Run ref.
- `AC-29`: `factory init --github owner/repository --credential github-main` creates a non-overwriting starter bundle that validates, renders deterministically, and completes manual plan, build, and verify against fake provider and runtime adapters after that credential is set.
- `AC-30`: Operator mutations reject missing or invalid credentials, and browser mutations reject missing CSRF state or a foreign origin.

## 10. Test approach

Protocol unit tests cover canonical config hashing, strict parsing,
source-lineage identity, UUID and provider identity rules, Stage result envelopes, schema
validation, prompt section boundaries, limits, and revision transitions. These
prove `INV-1` through `INV-4`, `INV-8`, `INV-11` through `INV-13`, `INV-16`,
`INV-21`, `INV-22`, `INV-25`, and `AC-2`, `AC-6`, `AC-8`, `AC-11`, `AC-12`,
`AC-14`, `AC-23`, `AC-25`, and `AC-26`.

SQLite transaction tests replay concurrent polling and manual admission, config
rename and removal, claim and complete Attempts under competing leases, cancel
during completion, retry lost Attempts, reconcile proposed, expected, and
unrelated remote heads, keep runtime and reconciliation budgets independent,
cover both available and exhausted runtime budget, accept duplicate answers,
and restart during every transition. These prove
`INV-2`, `INV-5` through `INV-7`, `INV-12`, `INV-15`, `INV-22`, `INV-24`
through `INV-26`, and `AC-3`, `AC-13`, `AC-20`, and `AC-24` through `AC-27`.

Worker integration tests run fake runtime adapters through plan, build, verify,
request changes, revision, approval, malformed output, timeout, cancellation,
lease loss immediately after push, non-fast-forward push, `needs_input`, and
recovery on another Worker. Git fixtures prove exact base, revision ancestry,
environment credential exclusion, exact-ref postflight, and head behavior.
These prove `INV-9`, `INV-10`,
`INV-18`, `INV-21`, `INV-23`, `INV-24`, `INV-25`, `AC-7` through `AC-14`, and
`AC-23`, `AC-24`, `AC-27`, and `AC-28`.

GitHub Connector contract tests use a fake provider API for actor permission,
timeline polling, rate limits, edits, repeated observations, acknowledgment,
progress, canonical manual admission, and idempotent pull-request creation.
These prove `INV-2`, `INV-13`, `INV-14`, `INV-17`, `INV-22`, and `AC-2` through
`AC-5`, `AC-15`, `AC-16`, `AC-22`, and `AC-25`.

CLI tests prove parsing, stable output, JSON isolation, durable request keys,
server-only finite commands, non-overwriting initialization, validation
diagnostics, deterministic rendering, credential redaction, and one-binary
process dispatch. A local integration test uses the initialized Source with
fake provider and runtime adapters for one complete manual Run. These prove
`INV-20`, `AC-1`, `AC-5`, `AC-19`, and `AC-29`.

React tests and one Playwright path against the real Go server prove the
monitoring fields, attention flows, cancel, retry, answer, source links, and the
absence of configuration mutation. API tests reject missing and invalid
operator credentials, missing CSRF state, and foreign origins. These prove
`AC-17`, `AC-18`, `AC-20`, and `AC-30`.

Migration tests open representative current databases and retained-worktree
fixtures with the new binary and prove byte-for-byte non-mutation and a clear
refusal. They prove `INV-19` and `AC-21`.

An opt-in end-to-end test against a scratch GitHub repository applies the label,
runs the full Pipeline on two Workers, forces one Worker loss after build,
recovers verify on the other Worker, and asserts one final draft pull request.

## 11. Risks and tradeoffs

- A fresh database loses unified historical views. The mitigation is an explicit backup, untouched legacy state, and continued access through the old binary.
- Pushing at mutating stage boundaries creates more remote refs. Factory uses one branch per Run and updates it with expected-head checks rather than creating one branch per Attempt.
- Structured output support differs by runtime. Factory owns one envelope, keeps adapter-specific parsing behind the runtime boundary, retries one malformed result, and records raw output for diagnosis.
- Config-only authoring is less approachable than browser forms. The mitigation is a shipped default, clear examples, `factory validate`, `factory render`, and an effective-config browser view.
- Polling has latency and consumes provider quota. The mitigation is bounded conditional requests, provider backoff, and durable cursors. Webhooks can be added after the local polling path is proven.
- Trusted-local execution cannot stop a same-user runtime from seeking ambient host credentials, including Git or operator credentials. Factory strips its environment and uses an isolated home but makes no containment claim; operators needing that guarantee run Workers under a dedicated OS identity until an isolated backend exists.
- One Run per item makes large batch progress less compact. The CLI prints a stable group of Run IDs, while a durable batch resource remains out of scope until real usage proves it necessary.
- A linear Pipeline cannot express every automation. Rejecting arbitrary graphs keeps transition, retry, and UI behavior understandable while covering the default engineering loop.

## 12. Open questions

- Should a dirty local `.factory/` tree be accepted? This does not block the architecture. Recommended default: allow it in an explicit `--dev` mode and freeze the exact bytes with `dirty = true`; require committed config otherwise.
- Should automatic infrastructure retries be configurable per stage? This does not block the first implementation. Recommended default: one retry for every stage, with later configuration only after failure data exists.

## 13. Out of scope

- Linear Agent Session implementation in the first release.
- GitHub webhook ingestion in the first release.
- Jira, GitLab, chat, email, or arbitrary external Connector plugins.
- Arbitrary DAGs, parallel stages, joins, conditional expression languages, or visual workflow editing.
- Multi-repository Runs, fleet Procedures, and durable batch parents.
- Browser editing of configuration or prompts.
- Automatic pull-request readiness, approval, merge, deployment, or release.
- A new coding model, agent framework, or subagent implementation inside Factory.
- Hostile-process isolation for local Workers.
- Cloud Run, Kubernetes, or other elastic execution backends.
- Automatic conversion or deletion of current Factory databases and retained worktrees.
