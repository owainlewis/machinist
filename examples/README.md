# Examples

The files at this level are the small default installed by `machinist init`:

- `config.toml` defines the shipped `foreman`, `audit`, and `shepherd` agents.
- `worker.toml` shows local Codex and Claude Code executors.
- `agents/` contains the editable default prompts.

`config.toml` includes a commented Shepherd schedule. Enable it only after its repository
name exists in `worker.toml`. Shepherd ensures the repository defines the
`machinist:auto-merge` label, but it never applies the label to a pull request. Unlabelled
pull requests remain read-only to Shepherd.

The same file also includes a disabled 24-hour PR risk-review trigger. Its
`pr-risk-reviewer` prompt classifies every open pull request as low, medium, or high risk
and merges only verified low-risk changes. Uncomment both blocks only when the configured
model is trusted to merge without a per-pull-request opt-in label. Replace the commented
GitHub repository mapping and add the same logical repository to `worker.toml` first.

The [workflow examples](workflows/README.md) are self-contained definitions with exact
setup and run commands:

- issue to pull request;
- Codex and Claude Code multi-review;
- read-only code audit and issue creation.

The [GitHub comment intake example](github-actions/README.md) safely turns a new,
authorized `@machinist` issue comment into a `machinist:requested` label for a
managed GitHub trigger.
