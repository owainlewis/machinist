# Factory files

The factory is a directory of plain files on the control plane, normally
`~/.machinist/factory`. It is read at startup. A change needs a restart, and an invalid
factory stops startup with a message that names the problem.

```text
factory/
  agents/<agent>/
    agent.toml              harness, model
    playbook.md             instructions for the agent
    .claude/agents/*.md     Claude Code subagents, in its own format
    .codex/agents/*.toml    Codex custom agents, in its own format
  jobs/<name>.toml          a list of stages
  reviewers/<name>.md       reviewer prompts
  automations/<name>.md     TOML front matter and a prompt
```

## Agents

```toml
harness = "claude"   # or "codex"
model = ""           # empty uses the harness default
```

The prompt for each run is the playbook, the branch name, the report format and the ticket
log, in that order. `default_agent` in `config.toml` picks the agent for new tickets.

Subagent files are written into each fresh clone and listed in `.git/info/exclude`, so they
never appear in a pull request. Machinist does not read or translate them. The harness finds
them the normal way. Give yours `machinist-` names so they do not replace a repository's
own.

## Jobs

A job is a list of stages that run in order, in one clone, on one runner. A stage is an
agent of the factory or a shell script.

```toml
# jobs/ship.toml
[[stages]]
name = "build"
agent = "default"

[[stages]]
name = "test"
script = "go test ./..."

[[stages]]
name = "review"
agent = "reviewer"
```

Each stage is a new process. An agent stage starts with a fresh context and the same ticket
log, and sees the work of earlier stages only in the clone. A script stage runs with `sh` in
the clone, with the same environment as the agent stages, and passes on exit code 0.

The run stops at the first stage that fails, and the stages after it are skipped. An agent
stage also stops the run when it reports `failed` or `needs_input`, which is how a review
stage rejects the work. There are no branches, loops or parallel stages. The run's time
limit covers all stages together.

The error of a failed script stage gives only its exit status. What the script printed is
on the run in the web app.

Every agent is also a job with one stage, so a job cannot share a name with an agent. All
agent stages of a job must use the same harness, and a job needs at least one agent stage.
To use a job, name it where an agent name goes: `default_agent` in `config.toml`, or `agent`
in an automation.

## Reviewers

Each file in `reviewers/` runs once, on a ticket's first pull request, with the ticket's
harness and model. The prompt is the file, then the repository and pull request number. The
reviewer gets no subagents and posts its findings with `gh pr comment`. To turn reviews off,
delete the files.

A reviewer is advice. If it fails, the failure shows on the ticket and is not retried,
because a retry could post twice.

## Automations

```markdown
+++
schedule = "0 9 * * 1-5"
timezone = "UTC"
repo = "owner/name"
agent = "maintainer"
+++
The prompt goes here.
```

`repo` must be in `github.repositories`, and `agent` must be an agent or a job. Missed runs collapse into
one, and no ticket starts while the automation's last one is active. An automation never
waits for a person: anything that would ask one fails the ticket instead, so one bad run
cannot stop its schedule. The Automations page has a Run now button.

`init` installs `factory/automation-templates/review-open-prs.md`, which merges pull requests
that are ready. It is off by default. To use it, copy it to `factory/automations/`, set
`repo` and restart. It uses the `maintainer` agent, because the default playbook forbids
merging. GitHub refuses any merge that branch protection does not allow.
