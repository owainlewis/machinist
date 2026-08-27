# Examples

The files at this level are the small default installed by `machinist init`:

- `config.toml` defines the shipped `foreman`, `audit`, and `shepherd` agents.
- `worker.toml` shows local Codex and Claude Code executors.
- `agents/` contains the editable default prompts.

`config.toml` includes a commented Shepherd schedule. Enable it only after its repository
name exists in `worker.toml`. Shepherd ensures the repository defines the
`machinist:auto-merge` label, but it never applies the label to a pull request. Unlabelled
pull requests remain read-only to Shepherd.

The [workflow examples](workflows/README.md) are self-contained definitions with exact
setup and run commands:

- issue to pull request;
- Codex and Claude Code multi-review;
- read-only code audit and issue creation.
