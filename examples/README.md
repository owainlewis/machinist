# Examples

`machinist init [directory]` copies these files to the current directory, or the one named:

- `config.yaml` configures the control plane.
- `runner.yaml` configures a local runner.
- `factory/prompts/` holds prompts, and `factory/scripts/` holds shell scripts. Every
  run carries both. Scripts are on `PATH`, and prompts are in `$MACHINIST_DIR/prompts`.
  `agent.sh <prompt>` runs Claude Code with `rules.md` and that prompt.
- `factory/presets/default.yaml` is the one preset: clone the repository, run Claude Code
  on the prompt, and open a draft pull request. It never merges. A preset is a list of
  shell steps; add another only when a kind of job needs different steps.
