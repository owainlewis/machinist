# Factory files

The factory is a directory of plain files on the control plane, normally `factory/` beside
`config.yaml`, in the same git repository. It is read at startup. A change needs a restart,
and an invalid factory stops startup with a message that names the problem.

```text
factory/
  presets/<name>.yaml       a list of steps
  agents/<name>.md          an agent: its harness, its model and its prompt
  prompts/<name>.md         text every agent shares, such as rules.md
  scripts/<name>            shell scripts that steps run
  automations/<name>.md     YAML front matter and a prompt
```

Keep what an agent is told in `agents/`, shell in `scripts/`, and presets to a short list
of steps that call them.

## Presets

A preset is a list of shell steps. A job names one preset, and every job of that preset runs
the same steps in the same order. `default_preset` in `config.yaml` picks the preset for
jobs that do not name one.

```yaml
# presets/default.yaml
description: Clone the repository, run Claude Code on the prompt and open one pull request.
timeout: 60m          # all steps together; optional

steps:
  - name: checkout
    timeout: 5m       # this step; optional
    run: checkout.sh

  - name: implement
    agent: implement   # runs factory/agents/implement.md

  - name: test
    idle_timeout: 30m # stop it after this long with no output; optional
    run: go test ./...
```

`description` is required: one sentence on what a job on this preset does. The New job form
shows it, with the steps, under the preset picker.

A step has either `run` or `agent`. `run` is shell; use `run: |` for more than one line.
`agent` names a file in `agents/`, and the step runs `machinist agent <name>`.

Each step runs with `sh -e` in one workspace on one runner. The workspace starts empty,
so cloning the repository is a step like any other. Steps run in order. The first command
that fails ends its step and the job, and the steps after it are skipped. There are no
branches, loops or parallel steps. To review and then fix, write the review step and then
the fix step.

Each step is a new process. Steps hand work on through files in the workspace.

A step that prints nothing for its `idle_timeout`, 15 minutes unless the step sets one, is
stopped and the job fails saying so. So a hung agent frees its runner slot instead of
holding it until the run's deadline. An agent prints a line for each message and action,
so only a single tool call that long stays quiet; give such a step a longer `idle_timeout`.
A step that hits either timeout gets SIGTERM and five seconds to exit cleanly before it is
killed. Usage a harness reported before then is kept; one that reports cost only when it
finishes, such as Claude Code, shows none.

### Agents

An agent file says which harness runs it, which model to ask for, and what to tell it:

```markdown
---
harness: codex        # agy, claude, codex or pi
model: gpt-5-codex    # optional; the harness's default otherwise
command: /opt/codex   # optional; the program to run, if it is not on PATH
---
Review the change on this branch...
```

`machinist agent <name>` gives the agent a system prompt in three parts: where it is
working (the repository, the branch and the control directory), then `prompts/rules.md`
if there is one, then the agent file's prompt. The job's prompt is the task. It runs the
harness headless with every permission, prints its progress as it goes, and records the
tokens and cost the harness reports. The step fails when the agent exits non-zero.

| Harness | Runs |
| --- | --- |
| `claude` | `claude --print --output-format stream-json` with the system prompt appended to Claude Code's own |
| `codex` | `codex exec --json`, with the system prompt first in the message, since Codex takes no separate one |
| `pi` | `pi --print --mode json --no-session` with the system prompt appended to Pi's own |
| `agy` | Google's Antigravity CLI: `agy --print <prompt> --output-format stream-json --dangerously-skip-permissions`, with the system prompt first in the prompt, since agy takes no separate one. Gemini models such as `gemini-4-neon-high`. agy reports tokens but no cost |

The web app shows which agent runs each step, on which harness and model: on the New job
form, in the job page's pipeline, and on running jobs on the board and the Runners page.
Machinist reads this from the step's `agent:` or from `machinist agent <name>` in its command
or in a script from `scripts/` that it runs. Once a step has run, it shows what actually ran,
which `machinist agent` records.

Each harness must be installed and signed in on the runner. To use a different agent for a
step, change `harness` in its file. To change what a step asks for, edit its prompt.

The shipped agents:

| Agent | Step |
| --- | --- |
| `implement` | Make the change with tests: a failing test first for a bug, the repository's own checks, and no claim that a check passed unless it ran. Commit it. Never push, open a pull request or merge |
| `review` | Review the branch without changing it, run the checks, and write `review.json`: a verdict of `accepted`, `revision_needed` or `human_decision_needed`, and findings marked unambiguous or not |
| `revise` | Fix the unambiguous findings and mark them addressed. `scripts/revise.sh` runs it only when the verdict is `revision_needed`, then runs `review` again |
| `publish` | Open one ready pull request with a summary, testing, the review and assumptions. It never pushes or merges. `scripts/publish.sh` pushes the branch first with `machinist push`, and runs it only when the branch has commits |

The review is worth most from a different model family than the one that wrote the
change. If Codex or agy is on your runners, set `harness: codex` or `harness: agy` in `agents/review.md`.

The last step, `machinist merge`, is code, not an agent. It merges the pull request with
`gh pr merge` when all of these hold, and otherwise leaves it open, says why in the result,
and the job waits as `pr_ready` for a person:

- `github.auto_merge` is on.
- `review.json` is valid and its verdict is `accepted`. A missing review leaves the pull
  request open, unless the step is `machinist merge --without-review` in a preset with no
  review step.
- Every check on the pull request passed. It waits for them, and for checks to start, up to
  two and a half minutes, before it treats the repository as having none. A failed check
  fails the job, even when the pull request would have stayed open anyway.

It merges with the first of a merge commit, a squash and a rebase that the repository
allows, so squash-only and rebase-only repositories work without a setting.

The reviser does not judge its own fixes. `scripts/review.sh` removes any `review.json`
before the review runs, and fails the step if the review writes none. `scripts/revise.sh`
keeps the first review as `first-review.json` and runs the review again on the fixed branch,
and only that verdict counts. When the first review accepted the change, no revise agent
runs.

`machinist verdict` prints the verdict, for scripts like these.

`machinist push` pushes the job's branch. When GitHub rejects the push, for example because
the token cannot change workflow files, the step fails with GitHub's answer. The job fails,
says why, and the merge step never runs. A job whose branch has no commits pushes nothing
and is done without a pull request.

When any step fails, the job's result is the step's error and the last line the step wrote
to stderr.

### Prompts and scripts

Every run carries the factory's `agents/`, `prompts/` and `scripts/` to the runner, so they
work on any runner, local or Kubernetes. They are written under `$MACHINIST_DIR`, and
`$MACHINIST_DIR/scripts` is first on `PATH`, so a step runs a script by its file name.
Together they must be under 4 MiB.

`prompts/rules.md` holds the rules for every agent: run headless, record guesses in
`assumptions.md`, follow the repository's instructions. `scripts/checkout.sh` clones the
repository and switches to the job's branch.

### Live output from an agent

`machinist agent` turns each harness's event stream into one line per message or action
as it happens:

```
Claude Code started with <model>
▸ Read internal/server.go
▸ Run go test ./...
Finished after 12 turns in 3m10s, $0.42
```

Lines that start with `▸` are actions. The job page shows the latest one as what a running
job is doing. `machinist agent` also records the tokens and cost the agent reports, so the
job page and the dashboard can count them. Codex and agy report tokens but no cost, so their
steps show the cost as not reported, and a total that leaves them out ends in `+`. An agent
with no `model:` shows the model its harness said it started with. To run some other command from
a `run` step with the same live lines, wrap it: `machinist agent-log -- <command>`. Output
that is not Claude Code's stream passes through unchanged.

Step names are letters, digits, hyphens and underscores, and are unique in a preset.

### What a step gets

| Variable | Value |
| --- | --- |
| `MACHINIST_REPO` | The job's repository, as `owner/name` |
| `MACHINIST_BRANCH` | `machinist/<job id>`, the branch for the job's work |
| `MACHINIST_JOB_ID` | The job's ID |
| `MACHINIST_PRESET` | The preset's name |
| `MACHINIST_DIR` | The control directory, outside the workspace |
| `GH_TOKEN` | The runner's GitHub token, for `gh` and `git` |
| `GIT_AUTHOR_*`, `GIT_COMMITTER_*` | The bot's name and email |

A step also gets the variables in the runner's `env` list. See
[Runners](runners.md#what-steps-can-use).

The prompt is the job's request. It is on stdin and
in `$MACHINIST_DIR/prompt.md`. A step need not read it.

### Control files

Steps talk to Machinist through files in `$MACHINIST_DIR`. It is outside the workspace,
so a commit never picks them up. Each file must be a regular file of at most 64 KiB.

| File | Effect |
| --- | --- |
| `result.md` | When every step passes, its text is the job's result. Without it the result is "Every step passed." |

Jobs run headless and nothing can answer a question mid-job. `prompts/rules.md` tells the
agent to make the smallest reasonable choice and list each one in `assumptions.md` in the
control directory. The publish step adds that list to the pull request under **Assumptions**.

After a job ends, Machinist looks for an open pull request on `MACHINIST_BRANCH` and records
its number on the job.

`init` installs one preset, `default`: clone, implement, review, revise, publish and merge. A job that names no preset uses it. Add a preset only when a kind of job
needs different steps, such as a separate test or review step.

## Automations

```markdown
---
schedule: "0 9 * * 1-5"
timezone: UTC
repo: owner/name
preset: default
---
The prompt goes here.
```

Quote `schedule`, because a cron line can start with `*`.

`repo` must be in `github.repositories`, and `preset` must name a preset. Missed runs
collapse into one, and no job starts while the automation's last one is active. An
automation never waits for a person: anything that would ask one fails the job instead, so
one bad run cannot stop its schedule. To run one now, call
`POST /api/v1/automations/<name>/run` with a token.
