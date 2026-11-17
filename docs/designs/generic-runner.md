# Machinist: One Workflow, Any Trigger

*   **Author:** Owain Lewis
*   **Status:** Accepted
*   **Date:** 2026-11-17

--------------------------------------------------------------------------------

## Overview

Machinist runs a predefined workflow on your own machines, the same way every time. You
do not prompt an agent and steer it. You assign work to a workflow that already encodes
how the work should be done.

A workflow is a **preset**: a straight list of steps. Each step is a shell command. A step
can run `claude -p`, `codex exec`, `opencode`, a test suite or `gh pr create`. Machinist
does not know or care which.

Anything can start a job with one HTTP call: a Linear agent app, a Jira webhook, a cron
job or `curl`. Runners poll the control plane for jobs, run the steps in one workspace and
report how it ended.

This design replaces the [redesign](machinist-redesign.md) and its GitHub-first model.

## Why

Machinist has no clear direction today. Most of its code exists to read GitHub, route
mentions, follow pull requests and parse the output of two specific harnesses. That makes
it large, tied to one tracker and one code host, and hard to explain.

The useful part is small: a queue of jobs, runners that claim them, and a fixed process
that every job goes through. This design keeps that part and removes the rest.

## Goals

*   One way to start work: `POST /machinist/run`. Every tracker is a caller, not a feature.
*   A workflow is a list of shell commands. Any agent, any tool, no harness code.
*   The same steps for every job of a preset, so results are consistent and comparable.
*   Runs on your machines: a laptop, a VM or Kubernetes.
*   A codebase small enough to read in an afternoon.

## Non-goals

*   **Loops, branches or a DAG.** To review and fix twice, list review, fix, review, fix.
*   **Streaming events.** A runner sends a log when a job ends, not while it runs.
*   **Talking to trackers.** The agent updates Linear or Jira itself, through MCP or a CLI.
*   **Talking to GitHub.** Steps use `gh` or `git`. The control plane never does.
*   **Follow-ups on pull requests.** A review comment or failing CI does not start a job by
    itself. A caller can start a new job for it.

## API

All routes need a bearer token.

| Route | Does |
| --- | --- |
| `POST /machinist/run` | Creates a job and returns it |
| `GET /machinist/jobs/{id}` | Returns a job, its state and its log |
| `GET /machinist/jobs` | Lists the newest jobs |
| `POST /machinist/jobs/{id}/input` | Answers a job that asked a question |
| `POST /machinist/jobs/{id}/cancel` | Stops a job |

A run request:

```json
{
  "key": "linear:ENG-142",
  "preset": "coding",
  "vars": { "repo": "owainlewis/airtime" },
  "prompt": "Add a retry to the upload client. See ENG-142."
}
```

`key` makes the call idempotent. A second call with the same key returns the first job.
`vars` must name every variable the preset declares, and nothing else.

## Jobs

| State | Meaning |
| --- | --- |
| `queued` | Waiting for a runner |
| `running` | A runner holds it |
| `needs_input` | A step asked a question and the job stopped |
| `done` | Every step exited 0 |
| `failed` | A step exited non-zero, timed out, or its runner was lost |
| `cancelled` | Someone cancelled it |

An answer to a question starts the job again from its first step, with the question and
answer added to the prompt. No workspace is kept between attempts, so a preset's first
step must work from nothing. This keeps runners stateless.

## Presets

A preset is a TOML file in the presets directory. Everything a job does is a step, as in a
CI pipeline. Machinist gives each job an empty workspace and runs the steps in order.
There are no hooks and no built-in clone: checking out a repository is a step like any
other.

```toml
timeout = "90m"

[[steps]]
name = "checkout"
run = 'gh repo clone "$MACHINIST_REPO" . && git switch -c "$MACHINIST_BRANCH"'
timeout = "5m"

[[steps]]
name = "plan"
run = 'claude -p "Write a plan to PLAN.md." < "$MACHINIST_DIR/prompt.md"'

[[steps]]
name = "test"
run = "just test"
```

The preset's file name is its name. Each step runs with `sh -e` in the job's workspace,
with:

| Name | Value |
| --- | --- |
| `MACHINIST_JOB_ID` | The job ID |
| `MACHINIST_PRESET` | The preset name |
| `MACHINIST_BRANCH` | `machinist/<job id>` |
| `MACHINIST_<VAR>` | Each var, upper-cased, such as `MACHINIST_REPO` |
| `MACHINIST_DIR` | A control directory outside the workspace |
| stdin and `$MACHINIST_DIR/prompt.md` | The prompt, and any questions and answers |

The control directory is outside the workspace so that `gh repo clone .` gets an empty
directory and a commit never picks up Machinist's files.

Steps hand work to each other through files in the workspace, such as `PLAN.md` and
`REVIEW.md`. Two files have a meaning to Machinist:

*   A step that writes `$MACHINIST_DIR/question.md` and exits 0 ends the job as
    `needs_input`.
*   The last step can write `$MACHINIST_DIR/result.md`. The job shows it, for example a pull
    request link.

A command that fails fails its step and the job, and no later step runs. A step with no timeout
gets the preset's remaining time.

## Runners

A runner polls only when it has a free slot. The protocol has four calls:

| Call | Does |
| --- | --- |
| `claim` | Returns a job and its preset, or nothing |
| `heartbeat` | Renews the lease and reports whether the job was cancelled |
| `finish` | Sends the outcome and the log |
| `register` | Says what the runner is and how many slots it has |

A lease lasts 10 minutes and heartbeats renew it. When a lease expires, the job fails with
`runner lost`. It is not retried, because a step may already have pushed or posted.

Credentials live on the runner, in its environment. The control plane holds only job data.

## Trackers

A tracker is a caller of the API. For Linear:

1.  An agent app or webhook calls `POST /machinist/run` with the issue as the key.
2.  The preset's agent steps have the Linear MCP server. They set the issue's status, comment
    and link the pull request.
3.  Machinist posts to the tracker only when the agent cannot: a failure, a timeout or a lost
    runner. That needs a callback URL on the job, added when the first adapter needs it.

Jira and others work the same way.

## The coding preset

The first preset, tested on `owainlewis/airtime`:

1.  Clone the repository and create a branch.
2.  Plan, writing `PLAN.md`.
3.  Implement the plan.
4.  Run the tests.
5.  Review the diff, writing `REVIEW.md`.
6.  Fix what the review found.
7.  Run the tests again.
8.  Push, open a pull request and write its link to `$MACHINIST_DIR/result.md`.

## What goes

*   GitHub comment polling, mention routing and permission checks.
*   Pull request and CI follow-ups, and reviewer runs.
*   `ghclient` and `fakegh`.
*   The Claude and Codex harnesses, their token parsing and run events.
*   The factory loader for agents, reviewers and playbooks.
*   Engine rules tied to pull requests and turn counts.
*   The MCP server. A tracker or coding session calls the API instead.
*   The pages of the web app built around GitHub.

## What stays

*   SQLite storage, with tickets renamed to jobs.
*   Runner polling, leases and slots.
*   Process supervision: timeouts, cancellation and process groups.
*   Token sign-in.
*   Schedules, as callers of the same job path.
*   The Kubernetes runner, with the same steps inside a Job.
*   A web app that lists jobs and shows each job's steps and log.

## Delivery

Each item is one pull request. Each leaves `main` working.

1.  This design.
2.  Delete the GitHub intake, follow-ups, reviewers and MCP server.
3.  The `/machinist/run` API and the job states above.
4.  The steps runner, replacing the harnesses, on local and Kubernetes runners.
5.  The coding preset, run on `owainlewis/airtime`.
6.  The web app: a job list and a job page with its log.

`ARCHITECTURE.md` and the guides change with the code, in the same pull request.

## Alternatives considered

**Keep GitHub as a source.** Mentions and follow-ups are useful, but they are most of the
code and tie Machinist to one host. A GitHub adapter can call the API later.

**Loops in presets.** A loop needs a stop rule, and the rule becomes a language. Repeating a
step is explicit and easy to read.

**Stream events from runners.** Live output is nice to have. A log at the end is enough to
see what happened, and the protocol stays four calls.

**Keep the workspace for answers.** Resuming a run where it stopped needs the runner to keep
state and the same runner to pick it up. Starting again is simpler and works on any runner.
