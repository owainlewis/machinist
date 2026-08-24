# Local Factory spike

This folder is a runnable reset of Factory. It tests one product idea:

> An always-on local control plane turns GitHub tickets into reviewed pull-request candidates, while one Foreman session owns the nuanced plan, build, verify loop.

It deliberately does not reuse the current orchestration packages. That lets us test the product shape before migrating the main application.

## Try it

Build the spike as `factory`:

```sh
go build -o /tmp/factory ./prototype/local-factory
```

Create a Factory project for a local checkout:

```sh
/tmp/factory init ~/Code/my-factory \
  --repo acme/payments \
  --repo-path ~/Code/payments
cd ~/Code/my-factory
```

This creates:

```text
my-factory/
  factory.toml
  agents/
    foreman.md
    planner.md
    builder.md
    verifier.md
```

Edit the TOML to select Claude, Codex, or a local command for each role. Edit the Markdown files to change the stable operating prompts.

`factory init` records the remote default branch as `base_ref`; every Work starts from that ref rather than whichever branch happens to be checked out. Pass `--base-ref` only when the default cannot be detected or you intentionally want another base.

Start the always-on process:

```sh
/tmp/factory web
```

Then open [http://127.0.0.1:7338](http://127.0.0.1:7338). The process remains in the foreground and owns:

- the GitHub label poller;
- admission and idempotency;
- the local scheduler and concurrency limit;
- embedded local workers;
- durable files under `.state/`;
- the monitoring UI;
- the future webhook HTTP boundary.

The poller admits every open issue with the configured `factory` label. An explicit run uses the same admission path:

```sh
/tmp/factory run acme/payments#123
```

`factory run` requires `factory web` to be running. It submits work to the control plane rather than starting a second runner.

## What one work item does

The scheduler claims the whole ticket once. It creates an isolated Git worktree and launches one Foreman session. The Foreman synchronously delegates to the configured agents:

```text
planner -> builder -> verifier
               ^          |
               +-- revise-+
```

The reports are normal Markdown. The verifier runs in a fresh detached worktree at the exact committed candidate revision. The Foreman interprets the report and either finishes, blocks, or asks the builder to revise. `max_revisions` bounds the loop.

The local issue snapshot, plan, build report, verification report, Foreman report, events, and checkout remain under `.state/` for inspection. `factory status` returns the same state as JSON.

## GitHub safety

Dry run is the default. Factory's GitHub adapter reads issues and labels, changes only isolated local worktrees, and does not edit issues, push branches, or open pull requests.

After proving a repository and its prompts locally, opt into writes explicitly:

```sh
/tmp/factory web --github-writes
```

In write mode, Factory publishes the plan to the issue, comments with the reason when work is blocked, pushes the candidate branch, and opens a draft pull request after verification.

This is a trusted-local executor, not a security sandbox. A custom `command` runtime, or another local agent configured with broad shell access, runs with the operator's host permissions and could use host credentials outside Factory's adapter. Use only trusted prompts and runtimes. Credential isolation belongs in the remote-worker phase.

## Current boundaries

- The process must be kept alive by the terminal, launchd, systemd, or a container. Service installation is not part of this spike.
- GitHub polling works now. `POST /webhooks/github` is reserved but returns `501` until signature verification and delivery deduplication are implemented.
- A process interruption marks an active attempt failed on restart. Automatic session resume is intentionally deferred.
- Running the same issue explicitly requeues failed or blocked Work as a new attempt. Polling never auto-retries terminal Work.
- The UI is read-only and local-only. The config rejects non-loopback listen addresses.
- Polling assumes that configured repositories and their trigger labels are controlled by trusted collaborators. Public-repository label-actor verification is not implemented yet.
- CI follow-up, PR review events, cloud workers, authentication, and multi-host leases are later steps. The Work and agent interfaces leave room for them without changing the prompt loop.

## Verify

```sh
go test ./prototype/local-factory
go vet ./prototype/local-factory
```

The end-to-end test creates a temporary Git repository, runs the real Foreman subprocess protocol with fake agents, forces one verifier-to-builder revision, and asserts the final committed candidate and retained artifacts.
