# Examples

`machinist init [directory]` copies these files to the current directory, or the one named:

- `config.yaml` configures the control plane.
- `runner.yaml` configures a local runner.
- `factory/agents/` holds the agents. Each file names its harness (Claude Code, Codex or
  Pi), an optional model, and the agent's prompt. `factory/prompts/rules.md` holds the
  rules every agent follows, and `factory/scripts/` holds shell scripts, on `PATH`.
- `factory/presets/default.yaml` is the one preset: clone the repository, have the
  `implement` agent make the change, then have the `publish` agent open a pull request
  and merge it once its checks pass, as `agents/publish.md` says. A preset is a list of
  shell steps; add another only when a kind of job needs different steps.
