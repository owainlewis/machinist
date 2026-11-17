# Examples

`machinist init` copies these files to `~/.machinist`:

- `config.toml` configures the control plane.
- `runner.toml` configures a local runner.
- `factory/presets/` holds the presets. A preset is a list of shell steps:
  - `default` clones the repository, runs Claude Code and opens a pull request. It never merges.
  - `maintainer` may merge, and is used by the merge automation.

`factory/automation-templates/` holds automations that are not switched on. To
use one, copy it to `factory/automations/`, set `repo` and restart the control
plane.
