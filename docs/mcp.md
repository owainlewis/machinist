# MCP

A bot or assistant can connect to Machinist over [Model Context Protocol](https://modelcontextprotocol.io) (Streamable HTTP, JSON-RPC 2.0) at `POST /mcp` on the control plane, using the same bearer tokens as the run API.

## Create a scoped token

Give each bot its own token so it can be revoked on its own. Create one in **Settings → Tokens** in the web app (`Bot — run & inspect (MCP / API)` or `Bot — read-only (MCP / API)`), or via the CLI:

```sh
# Can list jobs, list automations, read stats, inspect jobs and read output, plus run, rerun and cancel jobs.
machinist token create grokbot --scope run

# Read-only access: list jobs, list automations, read stats, inspect jobs and read output.
machinist token create watcher --scope read
```

Existing unscoped operator tokens keep full access, including MCP. Runner tokens cannot use MCP. Scoped tokens cannot sign in to the web app or manage tokens and projects.

Revoke one token without affecting any other:

```sh
machinist token revoke grokbot
```

## Connect

Send JSON-RPC 2.0 requests to `POST $MACHINIST_URL/mcp` with `Authorization: Bearer $TOKEN`:

```sh
curl -sS -X POST "$MACHINIST_URL/mcp" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## Tools

| Tool | Scope | Purpose |
| --- | --- | --- |
| `list_jobs` | `read` | List jobs newest first, optionally filtered by `repo`, `state` (`queued`, `running`, `waiting_for_ci`, `needs_you`, `done`, `failed`, `cancelled`), `source` and `limit`; includes optional `merge_gate` |
| `list_automations` | `read` | List automations with their schedule, `next_due` and `last_job` |
| `run_job` | `run` | Start a job by `automation` name, or a custom job with `repo` and `prompt` (optional `preset`, `key`) |
| `get_job` | `read` | Get a job's `state` (including `waiting_for_ci` and `needs_you`), pipeline `steps`, `current_step`, `latest_action`, `pr_url`, optional `merge_gate` (`decision`, `risk`, `reason`) and summary |
| `get_job_output` | `read` | Read decoded run output, optionally filtered by `step` or tailed incrementally with `after` |
| `cancel_job` | `run` | Cancel an active job; its pull request stays open |
| `rerun_job` | `run` | Start a new attempt for an ended job (`job_id`) |
| `get_stats` | `read` | Get Dashboard throughput, PR merge rate, cycle time and spend metrics (optional `days`, `repo`) |
