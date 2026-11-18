# Examples

`machinist init [directory]` copies these files to the current directory, or the one named:

- `config.toml` configures the control plane.
- `runner.toml` configures a local runner.
- `factory/presets/` holds the presets. A preset is a list of shell steps:
  - `default` clones the repository, runs Claude Code and opens a pull request. It never merges.
  - `coding` plans, implements, tests, reviews, fixes and tests again, then opens a draft pull request.
  - `maintainer` may merge, and is used by the merge automation.
  - `hello` clones nothing and runs no agent. It proves a runner works.

`factory/automation-templates/` holds automations that are not switched on. To
use one, copy it to `factory/automations/`, set `repo` and restart the control
plane.
