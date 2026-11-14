# Direction

Machinist is a job runner for coding agents. A team deploys agent jobs to one control plane.
The jobs run on machines the team controls, and everyone can see them.

This page says what that means, what is built, and what is missing. It is written to be
honest about the gaps, so it can be used to decide what to build next.

## Who it is for

A team that already uses coding agents and has hit one of these:

- Agents run in people's terminals, so nobody can see what the others have running.
- Work should run somewhere other than a laptop: a VM, or a Kubernetes cluster.
- The team wants the same steps every time, such as build, test, review, and wants to know
  how often they pass and what they cost.

One person on one machine gets less from it. A terminal already does that job.

## What it is

- **A control plane.** One Go server with a web app. It holds the jobs, talks to GitHub and
  never starts an agent.
- **Runners.** A runner asks the control plane for work and runs it, on its own machine or
  as Kubernetes jobs. Runners dial out, so one behind a firewall needs no inbound route.
- **Agents as files.** An agent is a playbook, a model, and its subagents and skills, in a
  folder. Nothing about an agent lives in the server's code.
- **Jobs as a list of stages.** An agent, another agent, a script. Stages run in order and
  the first failure ends the run.
- **A record.** Every run keeps its report, its stages, its tokens and its cost.

## What it is not

- Not an agent. It runs Claude Code and Codex. It does not replace them.
- Not a workflow engine. Stages do not branch, loop or run in parallel. A fix loop belongs
  inside one agent stage.
- Not a code review tool. Review happens on the pull request, in GitHub.

## What is built

Assigning work from the web app, a GitHub mention, MCP or a schedule. Follow-ups on the
same pull request when a person comments or CI fails. Questions and answers. Local and
Kubernetes runners. Jobs with stages. The web app: Home with usage, Work, Automations,
Runners and a chat page per job.

## What is missing

In the order I would close them.

1.  **Proof on real work.** The tests use a scripted agent and a fake `gh`. A job with
    several stages has not run on real Claude or Codex, and the Kubernetes runner and
    GitHub intake have not been run live from this code. Until they have, everything
    below is a guess about what matters.
2.  **Choosing the job.** You cannot pick a job or an agent when you assign work. New work
    uses `default_agent`, and only an automation can name another. A job runner needs a
    job picker.
3.  **Any harness.** The harness is a fixed choice of `claude` or `codex`. "Bring your own
    agent" is true for playbooks, subagents and skills. It is not yet true for a different
    runtime. The fix is a small harness contract and a plain `command` harness.
4.  **People.** A token has a name, and a job records who assigned it. There are no
    accounts or roles, no "my work" filter, and anyone signed in can cancel anything.
5.  **Usage by who and what.** Usage is a daily total. It is not split by person, job,
    agent or repository, and there are no spending limits.
6.  **Setup without a restart.** Adding a repository or changing the factory means editing
    a file and restarting the control plane.
7.  **Safety on a local runner.** A Kubernetes run is a sandboxed job. A local run is a
    process as the runner's user, holding the runner's GitHub token. Use a bot account
    with a token limited to the repositories it works on.
8.  **Telling you.** Nothing notifies you when a job needs an answer. You see it in the web
    app or on GitHub.
9.  **Running it again.** A failed job has no retry button.
10. **One set of words.** The web app says work and jobs. The API and the code say tickets
    and runs. A job is also the name of a pipeline file. These should become one
    vocabulary.

Jobs that do not belong to a repository are not planned. Every job today ends in a branch
and a pull request, and that keeps the system small.

## What would change this direction

If two people use it for a week and still start their agents in a terminal, the problem is
not a missing feature and this direction is wrong. If they use it and the complaints are in
the list above, the order of the list is the roadmap.
