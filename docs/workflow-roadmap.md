# Scale AI engineering through executable workflows

> Historical roadmap. For shipped stages, approvals, shared files and optional merge policies, see [the task guide](task-guide.md).


> Status: Proposed roadmap, 6 September 2026. This document describes future work. The current runtime and workflow examples are identified separately below.

## 1. Outcome and boundary

Machinist implements the **Scale** stage of the AI engineering lifecycle:

```text
Design → Plan → Build → Test → Review → Ship → Scale
```

Scale means building systems that build software: taking a repeatable engineering process out of individually supervised terminal sessions and encoding it as executable workflows. It is not a claim about scaling an application's traffic or a reason to run more agents before their output can be checked.

Blueprint owns the foundations: the lifecycle, teaching material, and reusable engineering practices. Machinist runs selected parts of that process through code. Its first supported workflow should take one ready GitHub issue to a verified PR or an explicit stopping point. Humans remain responsible for consequential requirements, acceptance, and release authority.

Scripts control stages, waiting, budgets, and recovery. Agents receive bounded phase assignments and return evidence. A workflow must not depend on loading a skill that decides what stage runs next. Project skills can still help an agent perform its assigned work, but they are optional execution context, not the workflow controller or a required Blueprint installation.

## 2. What exists today

The current [architecture](../ARCHITECTURE.md) deliberately separates execution from orchestration. The runner starts one approved process, supplies its input through stdin, streams output, captures artifacts and reported token usage, and enforces process timeout and cancellation. The control plane stores jobs and runs and leases work to capable workers. Each job has one run; process results determine terminal state.

There are two delivery examples:

- [The task-to-pr prompt](../examples/prompts/task-to-pr.md) asks one agent to implement a task in an isolated worktree, obtain a subagent review, open a PR, and repair CI and review feedback.
- [The issue launcher](../agent.py) accepts a GitHub issue URL, asks agents to implement and repair, polls CI, and limits repair passes. It starts a new coding session per call and has no durable workflow checkpoint.

These are useful experiments, not one supported contract. The issue launcher's argument input also differs from Machinist's stdin interface. Its control-flow tests do not demonstrate unattended delivery quality or high concurrency.

Keep the runtime boundary. Workflow stages and checkpoints belong to workflow code, not a new general-purpose graph engine inside the control plane.

## 3. The first supported workflow

### Input and authority

Accept a ready issue from a worker-approved GitHub repository. Resolve the issue, specification references, and existing work before changing files. A ticket states one outcome, acceptance criteria, exclusions, dependencies, and authority limits. Missing consequential decisions return to the human.

Use a direct CLI wrapper and a Machinist stdin adapter around the same workflow function. An input adapter must not create a second implementation of delivery logic. Other trackers can be added later behind the same task contract.

Worker configuration selects registered repositories and executor commands. The control plane can cap active jobs when configured, and the runner applies process deadlines. These settings do not isolate credentials or filesystem access: the shipped Codex and Claude executors have full host access. Repository registration controls admission, not everything an admitted process can reach.

Define and verify the execution boundary before M2. Use an isolated disposable environment with only the intended checkout and scoped test credentials. M1 must specify filesystem, credential, network, and resource restrictions, including how prohibited GitHub operations are denied; M2 proves those deployment controls. Issue text and review feedback remain untrusted task data. Instructions to ignore injected commands support the workflow, but do not enforce permissions that the process already holds.

### Code owns the lifecycle

The following is a conceptual sequence, not a claim that these functions already exist as one API:

```text
Resolve task and existing delivery
Prepare or recover isolated workspace
Run maker: implement and verify locally
Run independent checker
Open or update PR
Wait for configured CI and available review feedback
Classify feedback and repair valid problems within the budget
Recheck the changed result
Return ready-for-review or a precise stopping reason
```

Use one maker session across implementation and repairs when the harness supports it. Start a fresh checker with the ticket, relevant design, exact diff, and evidence; it must not edit the implementation. Persist durable artifacts so recovery does not depend on retaining a conversation. A new maker can continue from the verified work state when session recovery is unavailable.

The script makes phase calls and validates their outputs. It does not ask an agent to choose an orchestration strategy. Thin phase prompts specify the immediate task, constraints, expected result, and stopping rule. Changes to prompts are versioned with the workflow and tested against its scenarios.

### Evidence and stopping rules

Require explicit evidence for acceptance criteria, local verification, the reviewed head, and CI on the current PR revision. The checker can approve sound work and reject speculative or out-of-scope findings with supporting reasons. Passing tests alone cannot prove that the task was satisfied.

Configure the CI checks that establish readiness for each repository. The workflow waits for their registration and completion rather than interpreting an empty or incomplete visible set as success. If the expected set cannot be established, return a blocker. Additional review comments are collected within a bounded window; readiness does not claim that no future human review will arrive. Preserve unresolved known findings across polls.

The proposed initial policy is at most three code-repair passes per delivery, including resumes, with a 20-minute CI deadline per pushed revision and a configurable overall deadline. Exhaustion stops with evidence. Define how a reserved pass is reconciled after interruption before implementing recovery; a restart must not silently reset the budget.

Classify outcomes explicitly: ready for human review, needs a decision, blocked by environment or checks, failed execution, or cancelled. The runtime's process outcome remains separate from this workflow result. A blocked delivery may exit nonzero and remain a failed run in today's runtime while its artifact explains the actionable reason. Do not rewrite historical run outcomes to disguise retries.

The initial workflow never merges or deploys. A future shipping workflow must define release authority and target-specific verification separately.

## 4. Ownership

| Component | Owns |
| --- | --- |
| Human | Requirements, consequential decisions, acceptance, and release authority |
| Workflow script | Stage order, phase contracts, budgets, checkpoints, and feedback handling |
| Maker | Implementation, local tests, and scoped repairs |
| Checker | Independent assessment and supporting evidence |
| GitHub | Issues, PR heads, CI results, and review discussions |
| Machinist runtime | Process execution, leases, limits, logs, and artifacts |

Labels can advertise readiness or request intake. They are not a lock, proof of completion, or a replacement for durable identity. Never use two different controllers for the same active delivery.

## 5. Milestones and proof

### M1. Define one delivery contract and scenario suite

**Result.** Maintainers can identify the supported workflow, its required inputs, its outputs, and its failure behavior.

Inventory the existing examples against the lifecycle. Choose one canonical implementation and preserve useful mechanics from the others. Specify phase results, PR/head identity, configured checks, authority, deadlines, and the distinction between a repair and an infrastructure retry. Convert these decisions into a small technical design before writing the replacement workflow. Retain old examples with explicit experimental or migration status until their replacement is exercised.

Include the isolated deployment and access controls required for M2 in that design. Assign each restriction to an actual deployment, credential, or repository control and define a test that would expose its absence. Identify required capabilities the current runtime does not provide. A prompt or repository mapping alone cannot satisfy an access restriction.

Define M2's duplicate-run behavior too. Lease expiry can cause even the sole worker to receive the same run again after its stale completion is rejected. M2 must reject repeated execution before any agent or GitHub mutation; resumable execution is a later capability.

**Done when:** every stage has an owner and stopping rule; the workflow has no dependency on skill routing; a sample issue can be traced to evidence and final status without conflicting loops.

**Check:** independent architecture review and executable fixtures for success, a code defect, a correct change, a false review finding, missing CI, timeout, and missing authority. Review the deployment controls and their failure tests before authorizing the live trial. Fixtures are baseline tests, not claims of live reliability.

**Dependencies:** the agreed lifecycle. Schema details are settled here before M2 implementation.

### M2. Deliver one real issue through code

**Result.** The same workflow works directly and through Machinist.

M2 through M4 require a dedicated single-worker deployment: one control plane, exactly one worker instance, and one active workflow invocation. Run direct and managed trials separately. The current runtime already requeues expired 30-second leases for any matching worker, so limiting the number of tickets does not prevent cross-worker duplication. Do not use an existing multi-worker deployment for these milestones. Before retrying or resuming, confirm the previous worker and its child processes have stopped; otherwise remain blocked. Automatic failover and adding another worker require M5's ownership protections first.

Provision and verify M1's isolated environment before executing an agent on issue or review text. Keep host checkouts and production credentials unavailable to it. If a required access restriction cannot be demonstrated, stop the trial until the deployment provides it. This work belongs to the deployment setup; the roadmap does not claim that today's worker configuration supplies isolation.

Before starting the maker, atomically create a durable local admission record for the repository and task, including the managed run ID when present. Keep it outside agent-writable files. A repeated invocation, including lease redispatch, returns a blocked result without starting an agent, changing GitHub, or resetting a repair budget. Do not automatically expire or clear the record. An interruption may therefore need human intervention in M2; M3 adds controlled resume from evidence. Include this admission guard in both the direct and stdin paths.

Implement the canonical workflow with a stdin adapter, explicit maker/checker calls, bounded CI waiting, and feedback classification. Choose a tested local harness adapter first. Record workflow/prompt versions, executor version, PR/head identity, verification evidence, elapsed time, and reported usage. Keep stdout machine-readable and progress logs human-readable; retain full machine feedback as bounded-access artifacts rather than an endless terminal transcript.

**Done when:** a ready issue produces one PR with applicable evidence; a seeded defect is fixed and rechecked; optional or self-authored feedback does not create endless repair passes; a missing decision stops without inventing requirements. No merge occurs.

**Check:** unit tests of state transitions and a live run in an explicitly disposable repository. Verify the dedicated deployment has one worker and no overlapping direct invocation. Use non-sensitive fixtures to prove the configured access restrictions, including rejection of unauthorized GitHub operations and unavailable host files and credentials. Exercise at least one full repair cycle. Verify terminal logs, result artifacts, process cancellation, and token reporting. Compare the final behavior with the issue's criteria rather than trusting the agent summary.

Inject a heartbeat outage longer than the 30-second lease and let the worker receive the same run again after completion rejection. Confirm the second invocation is blocked before an agent call or GitHub mutation. Repeat after a process restart and with a duplicate direct invocation. No test may treat renewed execution with a fresh repair budget as success.

**Dependencies:** M1, its verified isolation controls, and the single-worker deployment restriction above.

### M3. Resume interrupted work on the same worker

**Result.** Restarting a delivery preserves useful work and its repair budget.

Extend M2's admission record into a versioned, workflow-owned checkpoint in durable worker storage. Track task identity, delivery identity, worktree and branch, PR and head, stage, repair reservations, and handled feedback. Use an exclusive per-delivery lock and atomic checkpoint replacement. Reconcile the checkpoint with Git and GitHub before proceeding; a checkpoint records claims that must be revalidated. Only the explicit resume path may reopen an admitted delivery, after the previous execution has stopped.

Test interruptions before and after commit, push, PR creation, and checkpoint writes. Preserve dirty or unpublished work. When identity or history conflicts, stop for a decision instead of creating a second PR or overwriting a branch. Retain the workspace while its PR is active; clean up only after merge or closure and verification that no unpublished work remains.

A resume is a new execution attempt associated with the same delivery. Preserve the runtime's one-job/one-run model and link attempts through workflow-owned identity. This milestone supports the same worker only; it does not claim cross-worker recovery.

**Done when:** the interruption scenarios resume the same PR, preserve the code and repair count, reject concurrent local attempts, and leave an understandable trace.

**Check:** fault injection at each side-effect boundary and a real interrupted/resumed delivery. Record cases that require human intervention rather than pretending every interruption is automatically recoverable.

**Dependencies:** M2; review the checkpoint and side-effect reconciliation design before coding it.

### M4. Make the system operable

**Result.** A person can see what is running and act on a blocked delivery without searching agent transcripts.

Expose the workflow stage and actionable result through logs and retained artifacts using the runtime's existing event capture. Show the issue, PR, current head, attempt, latest evidence, and stopping reason. Preserve the separation between process state and workflow state. Add a resume entry point that reuses M3 identity and state.

Classify environment failure separately from a code defect. Define limited retries for transient infrastructure errors under the overall deadline. A retry is recorded; a passed retry does not erase the earlier failure. Guard logs against secrets, terminal control characters, and excessive output.

**Done when:** an operator can locate the last verified revision, identify the blocker, cancel active work, and resume eligible work. Errors in monitoring must not produce an incorrect success result.

**Check:** operator walkthroughs for a timed-out agent, failed CI, missing credentials, and a successful repair. Confirm cancellation ends child processes and a subsequent resume reconciles the work.

**Dependencies:** M3. Extend the UI only where existing logs and artifacts cannot support these tasks.

### M5. Scale independent deliveries across workers

**Result.** Several ready tickets can progress without duplicate work or shared-workspace collisions.

Start with two independent deliveries, then four after the same checks pass. Each delivery owns an isolated workspace and PR. Dependencies wait for their agreed prerequisite condition; begin with merged prerequisites, then evaluate stacking as a separate feature if demand warrants it.

Before enabling a second worker, cross-worker retries, or automated intake, design shared delivery claims keyed by repository and issue. Local locks alone cannot stop two workers receiving separate jobs for the same ticket or the runtime reassigning an expired lease. Define ownership expiry, stale-worker behavior, reassignment, and reconciliation of remote effects. Reject stale state updates and prevent concurrent GitHub writes; terminal-result rejection alone does not stop a stale process from pushing code. Shared storage or state transfer for resume requires an explicit design at this stage.

Add issue-label or event intake only after duplicate delivery is handled. Bound active jobs, agent concurrency, rate-limit usage, and per-delivery cost. Preserve cancellation and useful capacity for unrelated jobs when one delivery is blocked.

**Done when:** duplicate events produce one active delivery; two workers cannot mutate the same delivery concurrently; worker loss produces a recoverable or explicit blocked state; capacity limits remain enforced under queued load.

**Check:** multi-worker duplicate-intake, expiry, network-partition, cancellation, and dependency scenarios. Publish the tested concurrency and workload. Do not claim arbitrary scale from a small successful demo.

**Dependencies:** M3 and M4. The shared-claim and stale-worker design must pass review and its protections must be implemented and verified before enabling a second worker, automatic failover, or event intake.

### M6. Publish a repeatable factory example

**Result.** A community member can practice the Scale stage on work they already understand.

Package the tested ticket-to-PR workflow, a small fixture repository, configuration, explicit permissions, and an operator guide. Show one successful delivery, one repair, and one interruption/resume. Explain how the code maps to the Blueprint lifecycle without requiring a Blueprint skill installation to run it.

Compare direct agent use and scripted execution on the same scoped tasks. Report correctness, human interventions, duration, cost, repair count, and failure cases with pinned versions. Include instructions for stopping and cleaning up disposable resources.

**Done when:** another person can reproduce the example and explain which decisions are human-owned, which checks are executable, and which failures require intervention.

**Check:** clean-environment rehearsal and learner walkthrough. Publish only measured outcomes and explicit limits.

**Dependencies:** M4 for a single-worker lesson; demonstrate multi-worker scale only after M5.

## 6. First implementation boundary

Start with M1 and M2. Reuse the existing runtime, build one dependable workflow, and exercise it before distributing work across machines. M3 and M4 make that workflow recoverable and operable. M5 earns unattended parallel execution.

The detailed phase schema, checkpoint format, shared-claim mechanism, and deployment storage are implementation-design decisions with explicit milestone gates above. This roadmap is not a substitute for those designs and does not claim that they have already been resolved.

## 7. Success measures and exclusions

Measure accepted outcomes, human interventions per delivery, defects missed, false repair passes, time and cost, recovery success, and duplicate-delivery incidents. Inspect per-case results. Throughput is useful only while those quality and authority properties hold.

The initial scope excludes autonomous product prioritization, a generic workflow language, required skill discovery, cross-tracker adapters, automatic merge/deployment, and a new runtime graph model. Those can be evaluated later against demonstrated needs.

## Related material

- [Current architecture](../ARCHITECTURE.md), [execution configuration](configuration.md), and [workflow examples](../examples/workflows/README.md).
- [Blueprint repository](https://github.com/owainlewis/blueprint): the lifecycle and improvement roadmap are being proposed there alongside this plan. They supply shared terminology and teaching material, not an implicit runtime dependency.
