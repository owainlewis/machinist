# MCP

The control plane serves MCP at `/mcp` on its normal address. It needs a token, like every
other route. Give each person their own:

```sh
machinist token create "$USER"
```

## Connect

Claude Code:

```sh
claude mcp add --transport http machinist http://10.0.0.5:7331/mcp --header "Authorization: Bearer <token>"
```

Codex, in `~/.codex/config.toml`:

```toml
[mcp_servers.machinist]
url = "http://10.0.0.5:7331/mcp"
bearer_token_env_var = "MACHINIST_TOKEN"
```

## Tools

| Tool | Does |
| --- | --- |
| `assign_ticket(request_id, repo, issue_number)` or `(request_id, repo, title, body)` | Creates a ticket and returns at once |
| `get_ticket(id)` | Reads one ticket |
| `list_tickets(state, repo)` | Lists the newest 200 tickets |
| `answer_ticket(id, text)` | Answers a waiting question |
| `cancel_ticket(id)` | Stops a ticket. Its pull request stays open |

Each returns the ticket: ID, state, link, pull request and any waiting question.

`request_id` makes a retry safe. The same ID returns the same ticket. A request for an issue
that already has an active ticket returns that ticket. Closing your session does not affect
the work.

The route keeps no session, so a control plane restart does not break a connected client.
