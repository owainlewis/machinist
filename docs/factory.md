# Factory files

The factory is a directory of plain files on the control plane, normally `factory/` beside
`config.toml`, in the same git repository. It is read at startup. A change needs a restart, and an invalid
factory stops startup with a message that names the problem.

```text
factory/
  presets/<name>.toml       a list of steps
  automations/<name>.md     TOML front matter and a prompt
```

## Presets

A preset is a list of shell steps. A job names one preset, and every job of that preset runs
the same steps in the same order. `default_preset` in `config.toml` picks the preset for
jobs that do not name one.

```toml
# presets/default.toml
timeout = "60m"            # all steps together; optional

[[steps]]
name = "checkout"
timeout = "5m"             # this step; optional
run = '''
gh repo clone "$MACHINIST_REPO" . -- --quiet
git switch "$MACHINIST_BRANCH" 2> /dev/null || git switch -c "$MACHINIST_BRANCH"
'''

[[steps]]
name = "test"
run = "go test ./..."
```

Each step runs with `sh -e` in one workspace on one runner. The workspace starts empty,
so cloning the repository is a step like any other. Steps run in order. The first command
that fails ends its step and the job, and the steps after it are skipped. There are no
branches, loops or parallel steps. To review and then fix, write the review step and then
the fix step.

Each step is a new process. Steps hand work on through files in the workspace.

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

The prompt is the job's log: the request, then each question and answer. It is on stdin and
in `$MACHINIST_DIR/prompt.md`. A step need not read it.

### Control files

Steps talk to Machinist through files in `$MACHINIST_DIR`. It is outside the workspace,
so a commit never picks them up. Each file must be a regular file of at most 64 KiB.

| File | Effect |
| --- | --- |
| `question.md` | When a step leaves it non-empty, the job stops after that step and waits for an answer. The answer starts the preset again from the first step |
| `result.md` | When every step passes, its text is the job's result. Without it the result is "Every step passed." |

Work that a step did not push is lost when the job stops, so ask before doing the work, or
push first.

After a job ends, Machinist looks for an open pull request on `MACHINIST_BRANCH` and records
its number on the job.

`init` installs four presets:

| Preset | Steps |
| --- | --- |
| `default` | Clone, run Claude Code on the prompt, open one pull request |
| `coding` | Clone, plan, implement, test, review, fix, test again, open one draft pull request with the plan and the review |
| `maintainer` | Clone, look after open pull requests, and merge the ready ones |
| `hello` | Print the job and its prompt, then finish. A smoke test with no clone and no agent |

The `coding` preset's test steps run `npm test`, `go test ./...`, `cargo test` or `pytest`,
by the files the repository has. Copy the preset and change those steps for anything else.

## Automations

```markdown
+++
schedule = "0 9 * * 1-5"
timezone = "UTC"
repo = "owner/name"
preset = "maintainer"
+++
The prompt goes here.
```

`repo` must be in `github.repositories`, and `preset` must name a preset. Missed runs
collapse into one, and no job starts while the automation's last one is active. An
automation never waits for a person: anything that would ask one fails the job instead, so
one bad run cannot stop its schedule. To run one now, call
`POST /api/v1/automations/<name>/run` with a token.

`init` installs `factory/automation-templates/review-open-prs.md`, which merges pull requests
that are ready. It is off by default. To use it, copy it to `factory/automations/`, set
`repo` and restart. It uses the `maintainer` preset, because the default preset never
merges. GitHub refuses any merge that branch protection does not allow.
