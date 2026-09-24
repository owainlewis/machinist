# Architecture

Machinist owns process execution and optional coarse workflow progression.
Commands own the work inside each step.

- `config.toml` defines portable named commands, optional prompt templates, timeouts,
  workflows, triggers, artifact storage, and server settings.
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

Tasks carry explicit requirements and an optional source URL. Each stage restores
the latest completed artifact snapshot into its output directory; workers publish
a new immutable snapshot after execution. The control plane stores the files on
disk in a configurable directory and keeps them until their task is deleted.
See [Artifact workflows](docs/artifacts.md) for the storage and file-handoff contract,
and [the task model](docs/task-artifact-model.md) for design rationale.
