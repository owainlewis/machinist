---
name: machinist
description: Assign work to Machinist and follow it. Use when a coding session should hand a GitHub issue or a piece of work to a Machinist agent, check a ticket, or answer its question.
---

# Machinist

Machinist takes a piece of work and returns a pull request. It runs the agent elsewhere, so
closing this session does not affect the work. It uses the `machinist` MCP server. See
`docs/mcp.md` to connect one.

## Assign work

Reuse an issue when the user gave one. Otherwise send the work as text, with the problem,
the outcome wanted and how to check it. Do not invent details the user did not decide.

Call `assign_ticket` with:

- `request_id`: a new unique value. Reuse it only when retrying the same request.
- `repo`: `owner/name`.
- `issue_number`, or both `title` and `body`.

It returns at once with a ticket ID and a link. Give both to the user.

## Check on it

Call `get_ticket` with the ID. States:

| State | Meaning |
| --- | --- |
| `working` | An agent is on it |
| `needs_you` | The `question` field says what it needs |
| `pr_ready` | A pull request is open. `pr_link` has it |
| `done` | Merged, or finished with no pull request needed |
| `failed` | An automation's run failed |
| `cancelled` | Someone cancelled it |

## Answer a question

Pass the user's decision to `answer_ticket`. Do not answer for the user unless they told you
how to decide. The ticket then continues on the same branch.

## Merging

Machinist's default agent never merges. Hand the pull request to a person.
