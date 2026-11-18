# Run API

Any system can start a job with one HTTP call: a tracker, a webhook, a cron job or `curl`.
Every route needs a token from `machinist token create`.

## Start a job

```sh
curl -sS -X POST "$MACHINIST_URL/machinist/run" \
  -H "Authorization: Bearer $MACHINIST_TOKEN" \
  -d '{
    "key": "linear:ENG-142",
    "preset": "default",
    "vars": {"repo": "owner/name"},
    "prompt": "Add a retry to the upload client. See ENG-142."
  }'
```

| Field | Meaning |
| --- | --- |
| `key` | Your ID for the request, at most 200 bytes. The same key returns the same job, so retries are safe |
| `preset` | An agent or job in the factory |
| `vars.repo` | A repository from `github.repositories`. It is the only variable today |
| `prompt` | The work. Its first line is the job's title |

The reply is the job, with `201` when it was created and `200` when the key already had one.

## Follow a job

| Route | Does |
| --- | --- |
| `GET /machinist/jobs` | Lists the newest jobs. `?repo=owner/name` filters them |
| `GET /machinist/jobs/{id}` | Returns one job |
| `POST /machinist/jobs/{id}/cancel` | Stops it. Its pull request stays open |

| State | Meaning |
| --- | --- |
| `queued` | Waiting for a runner |
| `running` | A runner holds it |
| `done` | It finished. `pr_number` is set when it opened a pull request |
| `failed` | It could not finish |
| `cancelled` | Someone cancelled it |

Jobs never stop to ask a question. Cancelling a job that has ended returns `409`. A missing
job returns `404`.
