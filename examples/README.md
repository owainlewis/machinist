# Examples

`machinist init` installs the files at this level:

- `config.toml` defines three commands and a commented trigger example.
  - `run` sends your instructions as the whole prompt, for one-off tasks.
  - `task-to-pr` takes a task or GitHub issue to a reviewed pull request.
  - `audit` finds verified correctness bugs and reports them as GitHub issues.
- `worker.toml` shows local Codex and Claude Code executors.
- `prompts/` contains the editable prompts for each command.

The [workflow examples](workflows/README.md) compare this prompt-driven style with a
workflow written as a script.

The [GitHub comment intake example](github-actions/README.md) turns a new, authorized
`@machinist` issue comment into a `machinist:requested` label for a managed GitHub trigger.
