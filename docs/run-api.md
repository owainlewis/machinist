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
| `GET /machinist/jobs` | Lists the newest jobs. `?repo=owner/name` filters them |
| `GET /machinist/jobs/{id}` | Returns one job |
| `POST /machinist/jobs/{id}/cancel` | Stops it. Its pull request stays open |
| `POST /machinist/jobs/{id}/rerun` | Runs an ended job again, on the same branch, and returns it |

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
