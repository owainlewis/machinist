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

`repo` must be in `github.repositories`, and `agent` must exist. Missed runs collapse into
one, and no ticket starts while the automation's last one is active. An automation never
waits for a person: anything that would ask one fails the ticket instead, so one bad run
cannot stop its schedule. The Automations page has a Run now button.

`init` installs `factory/automation-templates/review-open-prs.md`, which merges pull requests
that are ready. It is off by default. To use it, copy it to `factory/automations/`, set
`repo` and restart. It uses the `maintainer` agent, because the default playbook forbids
merging. GitHub refuses any merge that branch protection does not allow.
