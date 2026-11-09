# Examples

`machinist init` copies these files to `~/.machinist`:

- `config.toml` configures the control plane.
- `runner.toml` configures a local runner.
- `factory/` holds your agents and reviewers:
  - `agents/default` builds a ticket and opens a pull request. It never merges.
  - `agents/maintainer` may merge, and is used by the merge automation.
  - `reviewers/review.md` reads each ticket's first pull request.

`factory/automation-templates/` holds automations that are not switched on. To
use one, copy it to `factory/automations/`, set `repo` and restart the control
plane.
