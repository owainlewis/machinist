# Examples

`machinist init [directory]` copies these files to the current directory, or the one named:

- `config.yaml` configures the control plane.
- `runner.yaml` configures a local runner.
- `factory/prompts/` holds prompts, and `factory/scripts/` holds shell scripts. Every
  run carries both. Scripts are on `PATH`, and prompts are in `$MACHINIST_DIR/prompts`.
  `agent.sh <prompt>` runs Claude Code with `rules.md` and that prompt.
- `factory/presets/` holds the presets. A preset is a list of shell steps, usually one
  script each:
  - `default` clones the repository, runs Claude Code and opens a pull request. It never merges.
  - `coding` plans, implements, tests, reviews, fixes and tests again, then opens a draft pull request.
  - `maintainer` may merge, and is used by the merge automation.
  - `spec` clones the repository and returns a spec as the job's result, with the open
    decisions and a recommended answer for each. It opens no pull request.
  - `hello` clones nothing and runs no agent. It proves a runner works.

`factory/automation-templates/` holds automations that are not switched on. To
use one, copy it to `factory/automations/`, set `repo` and restart the control
plane.
