# Run API

Any system can start a job with one HTTP call: a tracker, a webhook, a cron job or `curl`.
Every route needs a token from `machinist token create`.

## Start a job

```sh
curl -sS -X POST "$MACHINIST_URL/machinist/run" \
  -H "Authorization: Bearer $MACHINIST_TOKEN" \
  -d '{
    "key": "linear:ENG-142",
    "vars": {"repo": "owner/name"},
    "prompt": "Add a retry to the upload client. See ENG-142."
  }'
```

| Field | Meaning |
| --- | --- |
| `key` | Your ID for the request, at most 200 bytes. The same key returns the same job, so retries are safe |
| `preset` | Optional. A preset in the factory. Without one, the job uses `default_preset`, which clones the repository, lets Claude Code do the work and opens one pull request |
| `vars.repo` | A repository from `github.repositories`. It is the only variable today |
| `prompt` | The work. Its first line is the job's title |

The reply is the job, with `201` when it was created and `200` when the key already had one.

## Follow a job

| Route | Does |
| --- | --- |
| `GET /machinist/jobs` | Lists the newest jobs. `?repo=owner/name` and `?source=linear` filter them |
| `GET /machinist/jobs/{id}` | Returns one job |
| `POST /machinist/jobs/{id}/cancel` | Stops it. Its pull request stays open |
| `POST /machinist/jobs/{id}/rerun` | Runs an ended job again, on the same branch, and returns it |

A job looks like this:

```json
{
  "id": "t_cc205867de0efc0e",
  "key": "linear:ENG-142",
  "source": "api",
  "preset": "default",
  "vars": {"repo": "owner/name"},
  "title": "Add a retry to the upload client. See ENG-142.",
  "state": "running",
  "current_step": "work",
  "tokens": 0,
  "cost_usd": null,
  "created_by": "linear",
  "created_at": "2026-11-22T15:00:00Z",
  "updated_at": "2026-11-22T15:01:00Z"
}
```

| Field | Meaning |
| --- | --- |
| `id` | Machinist's ID for the job. Use it in the routes above |
| `key` | The key the job was started with. A job that came from somewhere else, such as a schedule, shows that source's key |
| `source` | Where the job came from: `web` for the web app, `api` for a call with a token such as `curl`, `schedule` for an automation, or a tracker such as `linear`. See [job sources](designs/issue-sources.md) |
| `source_url` | The original issue or message, such as the Linear issue. Left out when there is none, as for a job started with `curl` |
| `preset`, `vars`, `title` | What the job runs, as in the request |
| `state` | Where the job is. See the table below |
| `current_step` | The preset step a running job is on. Empty when the job is not running, or has not started a step yet |
| `tokens` | Tokens the job's runs used, added up. It counts a run once the run ends |
| `cost_usd` | What the job's runs cost in US dollars, added up as runs end. `null` until a run reports a cost. Some harnesses report tokens but no cost |
| `pr_number`, `pr_state` | The job's pull request, once it has one |
| `created_by` | The token that started the job |
| `created_at` | When the job was made |
| `updated_at` | When the job last changed: it started running, opened a pull request, ended, or its pull request merged or closed |

| State | Meaning |
| --- | --- |
| `queued` | Waiting for a runner |
| `running` | A runner holds it |
| `done` | It finished. `pr_number` is set when it opened a pull request, and `pr_state` says whether that pull request is `open`, `merged` or `closed` |
| `failed` | It could not finish |
| `cancelled` | Someone cancelled it |

Machinist checks each open pull request on the job's branch every minute, so `pr_state`
changes within about a minute of a merge or close. It stops checking once the pull request
has merged or closed.

Jobs never stop to ask a question. Cancelling a job that has ended returns `409`, and so does
running a job again while it is still queued or running. A missing
job returns `404`.
