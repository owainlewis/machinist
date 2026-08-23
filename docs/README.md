# Factory documentation

Start with the root [README](../README.md) to run Factory.

## Current implementation

- [Architecture](../ARCHITECTURE.md): system boundaries, flows, contracts,
  security, limits, and source map.
- [Local guide](local.md): build, configure, start, delegate, and troubleshoot.
- [Phases](phases.md): versioned prompt files and the `factory` command line.
- [Worker contract](worker.md): identity, runtimes, claiming, process safety,
  and worktree cleanup.
- [Remote VM Workers](remote-workers.md): TLS listener, one-time enrollment,
  authentication, and reconnect behavior.
- [Tasks and Runs](tasks/design.md): implemented authoring, manual and
  scheduled Runs across repositories, lifecycle, and migration decisions.
- [Release guide](release.md): install, verify, upgrade, roll back, reproduce,
  and publish tagged releases.
- [Changelog](../CHANGELOG.md): user-visible changes and compatibility notes.
- [Security policy](../SECURITY.md): reporting and the current trust model.
- [Contributing](../CONTRIBUTING.md): setup, checks, and pull request standards.

## Project operations

- [Repository best-practices setup](resources/github/01-repository-best-practices.md):
  pasteable prompt for a safe, documented, agent-ready GitHub repository.
- [Issue-tracker setup](resources/github/02-issue-tracker.md): pasteable prompt
  for issue forms, labels, and a GitHub Project delivery board.

## Active design work

- [Agent-directed software factory](software-factory/design.md): proposed
  work-item queue, repository fleet Runs, agent update capability, and first
  useful CLI.
- [Cloud Run agent backend](cloud-run-agents/design.md): proposed elastic,
  API-backed execution alongside persistent local and VM Workers.
- [Software Factory vision](software-factory/vision.md): product thesis, scope,
  principles, and measures of progress.

## Design records and superseded proposals

- [Scheduled Automations](scheduled-automations.md): superseded operator guide
  for removed Definitions, Automations, and Definition Runs.
- [Product model upgrade](product-upgrade.md): completed migration record for
  converting supported legacy Definitions and Runs into the current model.
- [External GitHub ingest](github-ingest/design.md): replaced by control-plane
  typed Automations, then superseded by the target architecture.
- [Retired GitHub webhook settings](github-webhooks.md): upgrade note for the
  webhook listener removed with Definitions and Automations.
- [Reusable workflows and automations](workflows/design.md): design record for
  the implemented Workflow and typed Automation slices; superseded by Tasks and
  Runs.
- [Coding automation experience](automation-experience/design.md): implemented
  Runbook-first UX record, superseded by Tasks and Runs.
- [Unified CLI](cli/design.md): useful process-boundary record whose resource
  names and command contract are superseded by the agent-directed target
  design.

Current behavior belongs in the root `ARCHITECTURE.md`. Proposed behavior belongs
in a focused design until it is implemented.
