# Examples

`machinist init [directory]` copies these files to the current directory, or the one named:

- `config.yaml` configures the control plane.
- `runner.yaml` configures a local runner.
- `factory/agents/` holds the agents (`implement`, `review`, `revise`, `publish`, and
  `merge-gate`). Each file names its harness (`agy`, `claude`, `codex`, or `pi`), an optional
  model, and the agent's prompt.
- `factory/prompts/` holds shared prompts (`rules.md` for every agent, and `merge-policy.md`
  for `merge-gate`).
- `factory/scripts/` holds shell scripts on `PATH`.
- `factory/presets/default.yaml` is the default preset: `checkout`, `implement`, `review`,
  `revise`, `publish`, `merge-gate`, and `merge`. When `github.auto_merge` is `true`, low-risk
  approved pull requests merge once their checks pass. To run the same steps on another harness
  or model without copying agents or scripts, add a preset with `extends: default` and
  preset-level or per-step `harness:` and `model:` overrides:

  ```yaml
  description: Default steps on Antigravity (Gemini 4 Neon).
  extends: default
  harness: agy
  model: gemini-4-neon-high
  ```

`merge-policies/` holds starter `.machinist/merge-policy.md` templates (`conservative`,
`balanced`, `fast-startup`, and `strict-fintech`). See [Merge policies](../docs/merge-policies.md).
