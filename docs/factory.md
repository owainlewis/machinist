# Factory files

The factory is a directory of plain files on the control plane, normally `factory/` beside
`config.yaml`, in the same git repository. It is read at startup and reloaded on demand with
`machinist reload` — a restart is no longer needed after changing presets, agents, prompts,
scripts, automations, or reloadable `config.yaml` settings. An invalid factory stops startup,
or during `machinist reload` leaves the running control plane unchanged and reports the
problem. Runs already queued or running keep the job they started with.

Only `server.listen`, `server.database`, `server.tls_cert`, and `server.tls_key` in
`config.yaml` (plus process environment variables such as `GH_TOKEN` and `LINEAR_*`) still
need a control plane restart; `machinist reload` names any of those settings when they differ
and does not apply them.

```text
factory/
  presets/<name>.yaml       a list of steps
  agents/<name>.md          an agent: its harness, its model and its prompt
  prompts/<name>.md         text every agent shares, such as rules.md
  scripts/<name>            shell scripts that steps run
  automations/<name>.md     YAML front matter and a prompt
  manifest.yaml             hashes of the shipped files as last installed
```

Keep what an agent is told in `agents/`, shell in `scripts/`, and presets to a short list
of steps that call them.

## Upgrading shipped factory files

`machinist init` records the SHA-256 hash of each shipped file in `factory/manifest.yaml`.
When a newer Machinist binary ships updated factory files, the control plane warns at
startup and on the Settings page which files are out of date.

Run `machinist factory upgrade` from the configuration directory to apply them:

- Unedited files that match an older shipped hash are updated in place.
- Missing shipped files are created.
- Files your team has edited are never overwritten; Machinist prints a diff against the
  shipped version instead. Pass `--force` to overwrite edited files with the shipped copies.

Commit the updated files and `factory/manifest.yaml`, then run `machinist reload` (no restart needed).

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

### Extending presets and overriding harness or model

To run the same steps on another harness or model without copying agent prompts or
scripts, set `extends`, `harness` and `model` in a preset:

```yaml
# presets/neon.yaml
description: Default steps on Antigravity (Gemini 4 Neon).
extends: default
harness: agy
model: gemini-4-neon-high
```

`extends` inherits the base preset's steps, `timeout`, `harness`, `model` and (when omitted)
`description`. You can chain `extends` across presets, and override individual steps by name
— for example, to run a cross-model review without duplicating `review.sh`:

```yaml
# presets/neon-opus-review.yaml
description: Default steps on Antigravity with Claude Opus review.
extends: neon

steps:
  - name: review
    harness: claude
    model: opus
```

Override precedence is **step > preset > `agents/<name>.md` front matter**:

- Preset-level `harness` and `model` apply to every agent invoked inside the preset, whether
  the step uses `agent: <name>` or a shell script such as `review.sh`, `revise.sh` or
  `publish.sh`.
- Step-level `harness` and `model` override the preset for that step alone. The runner passes
  the effective values to the step as `MACHINIST_HARNESS` and `MACHINIST_MODEL`, which
  `machinist agent` reads.
- Switching `harness` without setting `model` drops any model inherited from the previous
  harness (and any custom `command:` in the agent file), so a model name from one harness is
  never passed to another.
- When extending a preset, a step in `steps:` whose `name` matches a base step updates that
  step in place (keeping its `run`/`agent` and timeouts unless replaced). A new step name
  must include `run` or `agent` and is appended at the end.

The web app, runner routing and Dashboard model metrics all resolve and report the effective
`harness` and `model` for every step.

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
if there is one, then the agent file's prompt. The job's prompt is the work. It runs the
harness headless with every permission, prints its progress as it goes, and records the
tokens and cost the harness reports. The step fails when the agent exits non-zero.

| Harness | Runs |
| --- | --- |
| `claude` | `claude --print --output-format stream-json` with the system prompt appended to Claude Code's own |
| `codex` | `codex exec --json`, with the system prompt first in the message, since Codex takes no separate one |
| `pi` | `pi --print --mode json --no-session` with the system prompt appended to Pi's own |
| `agy` | Google's Antigravity CLI: `agy --print <prompt> --output-format stream-json --dangerously-skip-permissions`, with the system prompt first in the prompt, since agy takes no separate one. Gemini models such as `gemini-4-neon-high`. agy reports tokens but no cost |

The web app shows which agent runs each step, on which harness and model: on the New job
form, on the job page, and on running jobs on the board and the Runners page.
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
| `merge-gate` | Judge the final diff against `.machinist/merge-policy.md` from `origin/HEAD` (or `prompts/merge-policy.md`), without changing files or GitHub credentials, and write `merge-gate.json` (`approve` + `low` risk, or `human_review`). `scripts/merge-gate.sh` skips branches with no commits or unaccepted reviews |

The review is worth most from a different model family than the one that wrote the
change. If Codex or agy is on your runners, set `harness: codex` or `harness: agy` in `agents/review.md`.

The last step, `machinist merge`, is code, not an agent. It reads `review.json` and `merge-gate.json` (when present), posts the merge gate judgment as a comment on the pull request, hands the
pull request to the control plane and ends at once, so the run is over and its runner slot is
free while CI runs. The result says whether the pull request will merge or stays open, and
why. The job is `pr_ready` while the control plane watches the checks, about once a minute.
It merges the pull request with `gh pr merge` when all of these hold, and otherwise leaves it
open for a person:

- `github.auto_merge` is on. The control plane checks its own setting, whatever a step says.
- `review.json` is valid and its verdict is `accepted`. A missing review leaves the pull
  request open, unless the step is `machinist merge --without-review` in a preset with no
  review step.
- `merge-gate.json` (required by `--require-gate` in the default preset, and enforced whenever present) is valid with `decision: "approve"` and `risk: "low"`. Medium- or high-risk changes stay open for human review with the judgment commented on the pull request.
- Every check on the pull request passed. The control plane waits for them, and for checks
  to start, up to two and a half minutes, before it treats the repository as having none. A
  failed check fails the job, even when the pull request would have stayed open anyway.

- The pull request's head is still the commit the merge step saw after the review, or the one
  the control plane's own branch update made from it. A commit pushed after the review
  leaves the pull request open for a person.

The merge names the head commit whose checks passed, so GitHub refuses any other. A branch
that fell behind its base is updated, up to three times, and every check must pass again on
the new head. It merges with the first of a merge commit, a squash and a rebase that the
repository allows, so squash-only and rebase-only repositories work without a setting. With a
GitHub App the control plane merges with the app's token, and otherwise with its own
`GH_TOKEN`, so that token needs to be able to merge.

### PR Steward

When a watched pull request fails a CI check (for example, a compile or test failure after a
parallel pull request merged) or hits a merge conflict (`DIRTY` merge state or a conflicting
branch update), **PR Steward** automatically shepherds the pull request back to green instead
of immediately failing or abandoning it:

- Up to `github.steward_retries` times per job (default `2`, `0` disables, max `5`, persisted
  in SQLite across control-plane restarts), the control plane clears the old check watch,
  records a timeline entry naming the failed check or conflict, and requeues a follow-up run on
  the **same branch**.
- The follow-up prompt tells the agent to fetch `origin`, merge `origin/HEAD`, resolve any
  conflicts, inspect and fix the failing check, run local checks, and commit on the branch so
  the run pushes the update and re-runs `review` and `merge-gate`.
- **Safety guardrails:** PR Steward never runs if a person pushed commits to the branch after
  the reviewed head commit, or if the pull request was already merged or closed.
- **Metrics:** Automatic steward retries are not counted as human `reruns` touches in Dashboard
  metrics. Once `github.steward_retries` is exhausted, Machinist falls back to marking the job
  `failed` (on CI failure) or leaving the pull request open (on merge conflict) and notifies
  a person.

The reviser does not judge its own fixes. `scripts/review.sh` removes any `review.json`
before the review runs, and fails the step if the review writes none. `scripts/revise.sh`
keeps the first review as `first-review.json` and runs the review again on the fixed branch,
and only that verdict counts. When the first review accepted the change, no revise agent
runs.

`machinist verdict` prints the verdict, for scripts like these. `machinist gate` validates and prints `merge-gate.json`.

### Merge gate and policy

The default preset runs a read-only `merge-gate` step (`scripts/merge-gate.sh`) after `publish` and before `merge`:

- **Blast-radius judgment:** Code quality is already checked by `review` and CI runs before merge. `agents/merge-gate.md` (pinned to `haiku` by default for low cost and latency) inspects `git diff origin/HEAD...HEAD` and decides whether the change is reversible, low-risk work (`decision: "approve"`, `risk: "low"`) or needs a person (`decision: "human_review"`, `risk: "medium"` or `"high"`).
- **Default policy:** `prompts/merge-policy.md` approves normal, reversible engineering work (features, UI updates, bug fixes, refactors, tests, documentation) and requires human review for high-blast-radius or hard-to-revert changes (database migrations/schema changes, auth/session/token/secrets logic, billing or breaking public APIs, CI/deployment/dependency upgrades, or weakening tests/safety checks).
- **Per-repository override:** Commit `.machinist/merge-policy.md` to a repository's default branch to override `prompts/merge-policy.md` for that repository. The agent reads it via `git show origin/HEAD:.machinist/merge-policy.md`, never from the PR branch, so a pull request cannot weaken its own policy. See [Merge policies](merge-policies.md) for copy-paste starter policies (`conservative`, `balanced`, `fast-startup`, `strict-fintech`).
- **Credential isolation:** `scripts/merge-gate.sh` unsets `GH_TOKEN` and `GITHUB_TOKEN` before running the judge agent, and skips the step when the branch has no commits or `review` did not accept.
- **PR comments & observe mode:** `machinist merge` posts the Merge Gate judgment as a comment on the pull request via `gh pr comment`. Setting `github.auto_merge: false` acts as **observe mode**: the gate still evaluates every pull request and comments its judgment without merging anything.
- **Opting out:** In a custom preset without a `merge-gate` step, use `machinist merge` (without `--require-gate`).

`machinist push` pushes the job's branch. When GitHub rejects the push, for example because
the token cannot change workflow files, the step fails with GitHub's answer. The job fails,
says why, and the merge step never runs. A job whose branch has no commits pushes nothing
and is done without a pull request.

The runner removes the workspace when the run ends, so a rejected push first saves the
change: `git format-patch` of the commits beyond `origin/HEAD`, written to `change.patch` in
the control directory. `format-patch` skips merge commits, so when the branch has one, for
example where the job merged the base branch in to resolve a conflict, the patch is one commit
of everything the branch changed since it forked from `origin/HEAD`, resolution included.
The control plane keeps it with the run, and the job page links to it. Download it and apply
it with `git am`, then push it yourself, without running the agent again. Any step can leave `change.patch` the same way. A patch over 4 MiB is not kept.

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
| `MACHINIST_HARNESS` | Effective harness override for the step, when set by the preset or step; read by `machinist agent` |
| `MACHINIST_MODEL` | Effective model override for the step, when set by the preset or step; read by `machinist agent` |
| `MACHINIST_AUTO_MERGE` | `"true"` or `"false"`, from `github.auto_merge` in `config.yaml` |
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

`init` installs one preset, `default`: `checkout`, `implement`, `review`, `revise`, `publish`,
`merge-gate`, and `merge`. A job that names no preset uses it. Add a preset only when a job
needs different steps, such as a separate test or review step.

## Automations

Automations can be created directly on the **Automations** page in the web app (stored in the control plane database) or checked into `factory/automations/<name>.md`:

```markdown
---
schedule: "0 9 * * 1-5"   # optional; omit for manual-only
timezone: UTC
repo: owner/name
preset: default
harness: codex            # optional; agy, claude, codex or pi
model: gpt-5-codex        # optional
---
The prompt goes here.
```

Quote `schedule`, because a cron line can start with `*`. Omit `schedule` (and `timezone`) for a manual automation that runs only when triggered by hand or via API.

`repo` must be in `github.repositories` (or added in Settings → Projects for UI-managed automations), and `preset` must name a preset. Missed scheduled runs collapse into one, and no job starts while the automation's last one is active. An automation never waits for a person: anything that would ask one fails the job instead, so one bad run cannot stop its schedule. To run one now, open **Automations** in the web app, or call `POST /api/v1/automations/<name>/run` with a token and an optional JSON body (`prompt`, `repo`, `preset`, `harness`, `model`) to override fields for that run only. Every run appears as a normal job on the **Jobs** page.
