# Agents and subagents

A run launches one fixed command for its harness. These flags turn off the harness's own
permission checks, so the OS user or gVisor Job is the boundary:

```text
claude --print --verbose --output-format stream-json --include-partial-messages
       --dangerously-skip-permissions --json-schema <report schema> --model <model>
codex app-server --stdio -c features.multi_agent=true -c sandbox_mode="danger-full-access"
```

Subagents come from the [factory](factory.md), and a repository can commit its own. Each
harness finds them the normal way:

- Claude Code: `.claude/agents/*.md`.
- Codex: `.codex/agents/*.toml`, with `name`, `description` and `developer_instructions`.

Machinist does not read, translate or route them.

## The report

The agent ends every run with one JSON object. Claude Code enforces its shape.

```json
{"outcome": "pr_ready|needs_input|no_change|failed", "summary": "...", "question": "..."}
```

## Reading agent activity

Open a ticket and choose Show activity on a run. It shows the agents that were used, their
roles and status, each agent's report, and the primary agent's messages. A local run streams
activity while it happens. A Kubernetes run shows it when the run ends.

## Live check

This proves that subagents load from the clone. It costs a small model call for each harness.

```sh
MACHINIST_NATIVE_LIVE=1 ANTHROPIC_API_KEY=... OPENAI_API_KEY=... go test ./internal/runner -run TestNativeLiveRepositoryAgents -v -count=1
```

Set `MACHINIST_NATIVE_LIVE_EXTENDED=1` to also check two parallel subagents and recovery from
a missing file.
