# Examples

`machinist init [directory]` copies these files to the current directory, or the one named:

- `config.yaml` configures the control plane.
- `runner.yaml` configures a local runner.
- `factory/agents/` holds the agents. Each file names its harness (Antigravity `agy`, Claude
  Code `claude`, Codex `codex` or Pi `pi`), an optional model, and the agent's prompt.
  `factory/prompts/rules.md` holds the rules every agent follows, and `factory/scripts/`
  holds shell scripts, on `PATH`.
- `factory/presets/default.yaml` is the default preset: clone the repository, have the
  `implement` agent make the change, review it and fix what the review found, then have
  the `publish` agent open a pull request, and `machinist merge` hand it to the control
  plane, which merges it once its checks pass and the review accepted it. To run the same
  pipeline on another harness or model without duplicating agents or scripts, add a preset
  with `extends: default` and preset-level or per-step `harness:` / `model:` overrides:

  ```yaml
  description: Default pipeline on Antigravity (Gemini 4 Neon).
  extends: default
  harness: agy
  model: gemini-4-neon-high
  ```
