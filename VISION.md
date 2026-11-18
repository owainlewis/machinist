# Vision

Machinist runs agent jobs the same way every time, on machines you control, where the
whole team can see them.

You give a job a repository and a prompt. Machinist clones the repository, runs an agent on
the prompt from start to finish, and opens one pull request. Nobody sits in a terminal
steering it. When the agent has to guess, it says so in the pull request.

## What we are building

- **One place for the team's agent work.** One control plane holds every job. Anyone
  signed in sees what is queued, what is running, each step's output, and the pull request
  at the end.
- **Jobs that run headless.** A job never stops to ask a question. It does the work, lists
  its assumptions, and hands a pull request to a person to review.
- **Runners anywhere.** A runner is a small daemon that asks the control plane for work. It
  runs on a laptop, a VM or Kubernetes, behind a firewall, with no inbound route. Runners
  keep no state between jobs.
- **Any trigger.** The web app's New job form, `curl`, a schedule, or assigning a Linear
  issue to the Machinist app all start a job through the same run API.
- **Narrow credentials.** Each run gets a GitHub token for its own repository that expires
  after an hour. Model and tracker keys stay on runners.

## Design rules

- **One default.** A job needs only a repository and a prompt. It runs the `default` preset:
  clone, run Claude Code on the prompt, open one pull request. Add another preset only when
  a kind of job needs different steps.
- **A preset is a straight list of shell steps.** A step runs any agent or command. Steps
  run in order in one workspace, and the first that fails ends the job. There are no loops,
  branches or gates inside a preset.
- **Files in git are the source of truth.** `config.yaml`, presets, prompts and scripts
  live in a repository and change by pull request. The web app shows a preset as a
  pipeline but never edits it.
- **Machinist does not merge.** A person reviews and merges every pull request. Branch
  protection and the GitHub token's permissions are the real limits on a step.
- **Simple over clever.** Fewer settings, fewer presets, short docs.

## What is not in scope

- An agent. Steps run Claude Code, Codex or any other command.
- A visual workflow editor or a general workflow engine.
- A tracker or code host. Agents push to GitHub and the job links to the pull request.

## Where it is

Built and merged:

- The run API, presets, local and Kubernetes runners, schedules, and headless jobs.
- YAML configuration in the repository, created by `machinist init`.
- The web app: jobs, each job's steps and output, and runners.

In review:

| Change | Pull request |
| --- | --- |
| Start a job when a Linear issue is assigned to the Machinist app | #604 |
| Dashboard of jobs, runs, success rate, time and cost | #605 |
| Each job drawn as a pipeline, and **Run again** for an ended job | #606 |
| New job form in the web app | #607 |
| Short-lived, one-repository tokens from a GitHub App | #608 |
| One-off jobs with only the `default` preset | #609 |

## Next

In this order, each only when real jobs show the need:

1.  **Rerun from a step.** Save each step's workspace so a failed job can start again at
    the step that failed instead of from the clone.
2.  **Declared step outputs.** A step names the files it produces, so the job page can show
    them and the next step can rely on them.
3.  **A preset as a step.** One step runs another preset, so a common sequence is written
    once.
