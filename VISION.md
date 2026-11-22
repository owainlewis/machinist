# Vision

Machinist is a job runner for coding agents. A job is a prompt, an agent and a host. You
run it by clicking **Run**, on a schedule, or from a trigger, and the whole team sees every
run, step by step.

Think of a Jenkins job, but each step is an agent working headless. "Triage open PRs" is a
file in your repository. It runs Codex on your VM every weekday at 9, and anyone can click
**Run** to start it now with a different prompt.

## What we are building

- **Saved jobs.** A saved job is a file in git with a prompt, a repository, an agent, a host
  and an optional schedule. The web app lists them, each with a **Run** button and its
  last result. Run takes the saved prompt or an edited one for that run.
- **Any trigger.** The **Run** button, a schedule, assigning a Linear issue, `curl` against
  the run API, or a bot through MCP. Every caller has its own revocable token.
- **One-off jobs.** The New job form takes a repository and a prompt and runs the defaults.
- **Any agent.** Claude Code, Codex, OpenCode, Pi or any other command. Each agent has a
  small adapter script that takes the prompt and runs headless. Machinist is not an agent.
- **Any host.** A runner is a small daemon on a laptop, a VM or Kubernetes. It asks the
  control plane for work, so it needs no inbound route. Runners carry labels such as `vm`,
  and report which agents they have. A job runs only where its host label and agent match.
- **One place for the team.** One control plane holds every job and run. Anyone signed in
  sees the dashboard, what is queued and running, what each running agent is doing right
  now, and the result.
- **Headless runs.** A run never stops to ask a question. It does the work, lists its
  assumptions, and ends with a result a person reviews: a pull request, comments or a
  report.
- **Narrow credentials.** Each run gets GitHub tokens for its own repository that expire
  after an hour. Model and tracker keys stay on runners.

## Design rules

- **Files in git are the source of truth.** `config.yaml`, saved jobs, presets, prompts
  and scripts change by pull request. The web app runs them but never edits them.
- **Ship no example jobs.** Machinist installs one preset, `default`, and no saved jobs.
  Each team writes the jobs it uses.
- **A preset is a straight list of shell steps.** Steps run in order in one workspace, and
  the first that fails ends the run. There are no loops, branches or gates inside a preset.
- **Jobs ship themselves.** The default preset has a second agent review the change, opens
  a pull request, waits for its checks and merges it when the review is clean. A team that
  wants a person to merge sets `auto_merge: false`. How it ships is a prompt in the factory that a team edits. Branch
  protection and the GitHub token's permissions are the real limits on a step.
- **Simple over clever.** Fewer settings, fewer presets, short docs.

## What is not in scope

- An agent. Adapters run agents that already exist.
- A visual workflow editor or a general workflow engine.
- A tracker or code host.

## Where it is

Built and merged:

- The run API, presets, local and Kubernetes runners, schedules, and headless jobs.
- YAML configuration in the repository, created by `machinist init`.
- One-off jobs with only the `default` preset, and the New job form.
- The web app: a dashboard, jobs as a board or a list, each job drawn as a pipeline with
  **Run again**, runners, and a Settings area with one page per topic, where repositories
  can be added as projects.
- Live runs. Each step's agent messages and actions stream to the job page as they happen.
  The board shows the current step and elapsed time, and each step records its agent,
  harness, model, tokens and cost. A failed job says why it failed.
- Short-lived, one-repository tokens from a GitHub App. A local runner takes a new one
  before each step.
- Tokens with roles: an operator token runs and watches jobs, a runner token takes work and
  reaches only its own runs. Each token can be revoked on its own.
- Agent files: each step names an agent in `factory/agents/`, which picks its harness
  (Claude Code, Codex, Pi or Antigravity), its model and its prompt.
- Harness matching. Runners report which harnesses they have, and a job goes only to a
  runner that has the agents it needs.
- Jobs that ship themselves: a second agent reviews the change, then Machinist opens the
  pull request, waits for its checks and merges it.
- Linear. Assigning an issue to the Machinist app starts a job in the repository its project
  or team maps to. Machinist moves the issue as the job goes, and stops the job when the
  issue is unassigned or leaves In Progress.
- Runs that stall stop. A step that prints nothing for its `idle_timeout` is stopped, and a
  run whose runner is lost is retried, at most three times in all.

Automations already hold a saved prompt, repository, preset and schedule, and can be run
through the API. They become saved jobs.

## Next

In this order. Each ships on its own.

1.  **Saved jobs in the web app.** Automations become saved jobs, listed on the Jobs page,
    each with **Run**, its schedule, its last result, and Run with an edited prompt.
2.  **An MCP server.** A bot such as a team assistant lists, runs, watches and cancels jobs
    through MCP, with a token of its own. Tokens get narrower scopes, such as run-only and
    read-only.
3.  **An OpenCode harness.**
4.  **Pick the host.** Runner labels and `runs_on:` on a job. A job with no label runs on
    any runner.
5.  **Retry a failed run.** Retry a run that failed with backoff, not only one whose runner
    was lost.
6.  **Rerun from a step.** Save each step's workspace so a failed run can start again at
    the step that failed instead of from the clone.
