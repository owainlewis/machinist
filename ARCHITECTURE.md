# Architecture

Machinist owns process execution and optional coarse workflow progression.
Commands own the work inside each step.

- `config.toml` defines portable named commands, optional prompt templates, timeouts,
  triggers, and server settings.
- `worker.toml` defines approved executor argument arrays and logical repository paths.
- `internal/runner` starts one process in one repository, writes the prompt to stdin,
  streams both output channels, records artifacts and token usage, and terminates the
  process tree on timeout or cancellation.
- `internal/controlplane` stores jobs and execution attempts, leases work to a capable worker,
  rejects stale completions, and exposes authenticated APIs and the web UI.
- `internal/managedworker` resolves only worker-owned executor and repository names.

Single-command jobs have one run and retain process-result semantics. Workflow
jobs snapshot an ordered command list and create one run per step attempt.
A valid step result advances, blocks, or fails the workflow in the same transaction
that records completion. Optional approval gates and explicit retries are persisted.
Workflow lease loss interrupts the job rather than replaying external effects.
See [Workflows](docs/workflows.md) for the result contract and recovery limits.

The [accepted task and artifact model](docs/task-artifact-model.md) defines the
next evolution: explicit task requirements and sources, per-execution output
directories, worker-published artifacts, and configurable durable storage and
retention. Those artifact capabilities are design decisions, not current runtime
features.
