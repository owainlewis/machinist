# Architecture

Machinist has one Go control plane with an embedded React UI, and any number of runners.
The control plane owns tickets, the ledger, schedules and the UI. It never starts
an agent. Runners do, and they dial out, so a runner behind a firewall needs no inbound
route.

```mermaid
flowchart LR
    Assign[Assign box or API] --> Intake
    Schedule[Automation file] --> Intake
    Intake --> Store[(SQLite tickets.db)]
    Store --> Engine[Ticket engine]
    Engine --> Queue[Run queue]
    Local[Local runner] -->|poll, heartbeat, events, complete| Queue
    Kube[Kubernetes runner] -->|poll, heartbeat, events, complete| Queue
    Local --> Agent[Agent and gh in a fresh clone]
    Kube --> Job[gVisor Job per run]
    Agent --> GitHub[GitHub: branch, PR, comments]
    Job --> GitHub
```

## Tickets

A ticket is one piece of work with one fixed branch, `machinist/<ticket id>`. Every run
starts from a fresh clone and checks out that branch if it exists. The only state carried
between runs is the pushed branch and the ticket log: the request, agent reports and human
answers. The agent reads anything else with `gh`.

Each run ends with one report: `pr_ready`, `needs_input`, `no_change` or `failed`. After a
run, the control plane looks up the branch's pull request. The agent does not report it,
so a crash or lost runner cannot create a second one.

| State | Meaning |
| --- | --- |
| `working` | An agent is running or about to |
| `needs_you` | Waiting for a person |
| `pr_ready` | A pull request is open and nothing is running |
| `done` | Finished with no pull request needed |
| `failed` | An automation's run failed. Automations never wait for a person |
| `cancelled` | Someone cancelled it. Its pull request stays open |

A ticket stops and asks a person after `max_turns_without_human` agent runs with no human
input. Each run has a deadline. Provider spend limits cap total cost.

The full state table is in the
[design](docs/designs/machinist-redesign.md#ticket-states), and every row has a test.

## Intake

Work comes from the Assign box, the [run API](docs/run-api.md) at `POST /machinist/run` and
automation schedules. The run API shows tickets as jobs. Each
request has a unique source key, so sending one twice has no effect. Machinist does not read
GitHub comments or follow pull requests. A ticket stays `pr_ready` once its pull request is
open.

Only the control plane posts Machinist's own messages, from rows it has stored. A crash
between posting and recording can post a message twice.

## Runners

A runner holds a token and polls only when it has a free slot. Four routes connect it to
the control plane: poll, heartbeat, events and complete. A run has a 10-minute lease stored
in the database and renewed by heartbeats. So runs outlive a control plane restart, and a
lost runner's ticket continues on any other runner once its lease expires.

Each poll also says what the runner is: local or Kubernetes, its harnesses and its slots.
The control plane stores that with the time of last contact, and heartbeats keep the time
current while every slot is busy. The Runners page reads it with the running runs and the
queue, so it is the same after a control plane restart.

| Runner | Where the agent runs | Its limits |
| --- | --- | --- |
| Local | A process with a private home and an allowlisted environment | The OS user and the GitHub login |
| Kubernetes | One gVisor Job per run, through an egress proxy | The Job, the proxy's host list and the GitHub login |

## Credentials and the GitHub login

Agents use `gh` directly, with the bot's token. They push branches and open pull requests
themselves. Model credentials live on runners, never on the control plane. Branch protection
and the login's permissions are what limit an agent. At startup the control plane refuses a
token that is not the bot's, and warns about an unprotected default branch or admin rights.

## Packages

```text
internal/tickets       tickets, runs, tokens, ledger, in SQLite
internal/engine        the state table, run queue, leases and posted messages
internal/sources       schedules and the one intake path
internal/ghclient      gh, with no shell
internal/factory       loads agents, jobs and automations from files
internal/controlplane  HTTP routes, sign-in and the embedded UI
internal/agentrunner   the runner daemon, and the local and Kubernetes executors
internal/kuberunner    gVisor Jobs, egress proxy and the code that runs in the Job
internal/runner        fresh clones, the fixed harness commands and event parsing
```
