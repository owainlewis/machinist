# Factory files

The factory is a directory of plain files on the control plane, normally `factory/` beside
`config.yaml`, in the same git repository. It is read at startup. A change needs a restart,
and an invalid factory stops startup with a message that names the problem.

```text
factory/
  presets/<name>.yaml       a list of steps
  prompts/<name>.md         prompts that steps pass to an agent
  scripts/<name>            shell scripts that steps run
  automations/<name>.md     YAML front matter and a prompt
```

Keep prompts in `prompts/` and shell in `scripts/`, and keep presets to a short list of
steps that call them.

## Presets

A preset is a list of shell steps. A job names one preset, and every job of that preset runs
the same steps in the same order. `default_preset` in `config.yaml` picks the preset for
jobs that do not name one.

```yaml
# presets/default.yaml
timeout: 60m          # all steps together; optional

steps:
  - name: checkout
    timeout: 5m       # this step; optional
    run: checkout.sh

  - name: work
    run: agent.sh work

  - name: test
    run: go test ./...
```

`run` is shell. Use `run: |` for more than one line.

Each step runs with `sh -e` in one workspace on one runner. The workspace starts empty,
so cloning the repository is a step like any other. Steps run in order. The first command
that fails ends its step and the job, and the steps after it are skipped. There are no
branches, loops or parallel steps. To review and then fix, write the review step and then
the fix step.

Each step is a new process. Steps hand work on through files in the workspace.

### Prompts and scripts

Every run carries the factory's `prompts/` and `scripts/` to the runner, so they work on
any runner, local or Kubernetes. They are written to `$MACHINIST_DIR/prompts` and
`$MACHINIST_DIR/scripts`, and `$MACHINIST_DIR/scripts` is first on `PATH`, so a step runs a
script by its file name. Together they must be under 4 MiB.

The shipped scripts:

| Script | Does |
| --- | --- |
| `checkout.sh` | Clones the repository and switches to the job's branch |
| `agent.sh <prompt>` | Runs Claude Code with `prompts/rules.md` and `prompts/<prompt>.md` as the system prompt and the job's prompt as the message |
| `test.sh` | Runs `npm test`, `go test ./...`, `cargo test` or `pytest`, by the files the repository has |
| `publish.sh` | Pushes the branch and opens one draft pull request, with `plan.md`, `assumptions.md` and `review.md` as its body when they exist |

To use another agent, change `agent.sh`. To change what a step asks for, edit its prompt.

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
control directory. `publish.sh` adds that list to the pull request under **Assumptions**.

After a job ends, Machinist looks for an open pull request on `MACHINIST_BRANCH` and records
its number on the job.

`init` installs five presets:

| Preset | Steps |
| --- | --- |
| `default` | Clone, run Claude Code on the prompt, open one pull request |
| `coding` | Clone, plan, implement, test, review, fix, test again, open one draft pull request with the plan and the review |
| `maintainer` | Clone, look after open pull requests, and merge the ready ones |
| `spec` | Clone, then write a spec for the request as the job's result. It opens no pull request |
| `hello` | Print the job and its prompt, then finish. A smoke test with no clone and no agent |

The `coding` preset's test steps run `test.sh`. Change that script for anything else.

For work that needs decisions from a person, run `spec` first. Its result lists the open
decisions with a recommended answer for each. Edit the spec and send it as the prompt of a
`coding` job.

## Automations

```markdown
---
schedule: "0 9 * * 1-5"
timezone: UTC
repo: owner/name
preset: maintainer
---
The prompt goes here.
```

Quote `schedule`, because a cron line can start with `*`.

`repo` must be in `github.repositories`, and `preset` must name a preset. Missed runs
collapse into one, and no job starts while the automation's last one is active. An
automation never waits for a person: anything that would ask one fails the job instead, so
one bad run cannot stop its schedule. To run one now, call
`POST /api/v1/automations/<name>/run` with a token.

`init` installs `factory/automation-templates/review-open-prs.md`, which merges pull requests
that are ready. It is off by default. To use it, copy it to `factory/automations/`, set
`repo` and restart. It uses the `maintainer` preset, because the default preset never
merges. GitHub refuses any merge that branch protection does not allow.
