# Direction

Machinist runs one predefined workflow on machines you control, the same way every time.
You assign work to the workflow. You do not prompt an agent and steer it.

The full design is [One Workflow, Any Trigger](designs/generic-runner.md). This page says
what that means and where the code is today.

## What it is

- **One API.** `POST /machinist/run` starts a job. Linear, Jira, cron or `curl` can call it.
- **Presets.** A preset is a straight list of shell commands. A step can run any agent or
  any tool. Steps hand work to each other through files.
- **Runners.** A runner claims a job, runs the steps in one workspace and sends the log.
  Runners run on a laptop, a VM or Kubernetes.

## What it is not

- Not an agent. Steps run Claude Code, Codex or anything else.
- Not a workflow engine. Steps do not loop or branch. Repeat a step to do it twice.
- Not a tracker or code host client. Agents update the tracker and push to GitHub
  themselves.

## Where the code is

The code still has the GitHub-first model from the
[earlier design](designs/machinist-redesign.md). It moves to the new design in these
steps, one pull request each:

1.  Delete the GitHub intake, follow-ups, reviewers and MCP server.
2.  Add the `/machinist/run` API and the new job states.
3.  Replace the Claude and Codex harnesses with the steps runner.
4.  Add the coding preset and run it on a real repository.
5.  Reduce the web app to a job list and a job page with its log.

## What would change this direction

If the coding preset does not produce pull requests worth merging on real work, the
problem is the workflow, not the runner. Fix the preset before adding features.
