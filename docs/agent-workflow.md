# Canonical issue delivery workflow

[`agent.py`](../agent.py) is the single issue-to-PR workflow. It accepts one GitHub
issue, implements it, verifies the returned PR relationship, waits for CI and review
feedback, and runs up to three repair passes. It never merges or force-pushes.

Codex and Claude are adapters beneath the workflow. They receive the same phase
prompts and must return the same JSON result contract.

## Run directly

```sh
./agent.py https://github.com/owner/repository/issues/123
./agent.py --prompt="Complete https://github.com/owner/repository/issues/123"
./agent.py --provider claude --model opus https://github.com/owner/repository/issues/123
```

The defaults can also be set with `AGENT_PROVIDER` and `AGENT_MODEL`.

## Run through Machinist

Machinist sends task input on stdin, so its executor points directly at the same
script. Configure machine-local executable paths and model aliases in
`worker.toml`:

```toml
[executors.agent-codex]
command = ["/absolute/path/to/machinist/agent.py", "--provider", "codex", "--model={{machinist.model}}"]
models = { sol = "gpt-5.6-sol", terra = "gpt-5.6-terra" }

[executors.agent-claude]
command = ["/absolute/path/to/machinist/agent.py", "--provider", "claude", "--model={{machinist.model}}"]
models = { sonnet = "sonnet", opus = "opus" }
```

Select the adapter in shared configuration:

```toml
[commands.deliver]
executor = "agent-codex"
timeout = "120m"

[github.repositories]
neo = "owainlewis/neo"

[triggers.github.issue-delivery]
every = "5m"
label = "machinist:requested"
command = "deliver"
```

Both of these enter the same Python workflow:

```sh
./agent.py https://github.com/owainlewis/neo/issues/123

machinist run \
  --command=deliver \
  --repo=/absolute/path/to/neo \
  --prompt="Complete https://github.com/owainlewis/neo/issues/123"
```

In managed mode, adding `machinist:requested` to an eligible issue queues the
same command. The trigger supplies `Complete <issue-url>` on stdin; `agent.py`
normalizes that to the same canonical URL used by direct invocation.

## Feedback and measurements

Passed CI with prose review feedback gets one structured assessment before repair.
An approval skips the coding turn after verifying the PR head is unchanged.
Findings or uncertainty enter the bounded repair workflow; failed checks and inline
comments enter it directly. Assessment instructions prohibit tools; Codex uses a
read-only sandbox and Claude disables tools with a one-turn limit.

The final `Metrics:` log separates implementation, feedback waiting, assessment,
and repair seconds. `repair_calls` counts coding invocations; `code_repairs` counts
passes that produced a new PR head, and `no_change_repairs` counts unchanged heads.
These are commit-based measurements, not proof of code quality.

Usage records input (including cached input), cached input, cache writes, and output
separately. Codex uses the last cumulative usage event in each fresh thread; Claude
uses result usage with cache reads/writes added to input. Missing usage makes total
tokens null and `usage_complete` false, and leaves the runner usage file empty.
Provider-reported usage is not a billing estimate and may exclude delegated work.

For Neo workers, run `bash scripts/setup-neo-worker.sh /absolute/path/to/neo` as
the worker account. It installs the linter pinned in Neo's CI and checks Go, Git,
and GitHub CLI availability. Put the reported binary directory on the service PATH
(or install that binary into `/usr/local/bin`). Repeat when the CI pin changes.
Each delivery also checks required executables and the workflow's linter pin before
launching the implementation agent. A missing or mismatched tool fails the task
with a provisioning error before spending model tokens.
