# Start jobs from Linear

Machinist is a user in Linear. Assign an issue in a connected team to the Machinist app,
and Machinist starts a job in that team's repository. The job's prompt is the issue's title,
description and link. Each issue starts one job, ever: assigning it again, or Linear
retrying a delivery, returns the first job.

## Set it up

1. In Linear, open **Settings → API → Applications** and create an app called Machinist.
   - Switch on its webhook. URL: `https://<your host>/webhooks/linear`. Resource: **Issues**.
   - Install it in the workspace with `actor=app`, so it appears as a user you can assign.
2. Find the app's user ID. With the app's access token, run the GraphQL query
   `{ viewer { id } }` against `https://api.linear.app/graphql`.
3. Copy the webhook's signing secret into `LINEAR_WEBHOOK_SECRET` in the control plane's
   environment, next to `GH_TOKEN`. Never put it in `config.yaml`.
4. Add a `linear` section to `config.yaml` and restart the control plane:

```yaml
linear:
  app_user_id: <the app's user ID>
  preset: coding
  teams:
    ENG: owner/name
  max_jobs_per_hour: 20
```

| Key | Meaning |
| --- | --- |
| `app_user_id` | The Machinist app's Linear user. Assigning an issue to it starts a job |
| `preset` | The preset Linear jobs use. Defaults to `server.default_preset` |
| `teams` | Linear team key to repository. Each repository must be in `github.repositories`. Issues from other teams are ignored |
| `max_jobs_per_hour` | The most jobs Linear can start in any hour. Defaults to 20 |

Startup stops if the secret is missing or shorter than 16 characters, `app_user_id` is
empty, a team's repository is not connected, or the preset does not exist.

## Expose only the webhook

`server.listen` should stay private, because it serves the UI and the API. Put a reverse
proxy in front that forwards only `/webhooks/linear`. With Caddy:

```text
machinist.example.com {
  handle /webhooks/linear {
    reverse_proxy 127.0.0.1:7331
  }
  respond 404
}
```

## What protects the route

The route needs no Machinist token, so every request is checked:

- **Signature.** `Linear-Signature` must be the HMAC-SHA256 of the raw body with the
  signing secret. The comparison takes constant time.
- **Freshness.** `webhookTimestamp` must be within one minute of now, so a captured
  delivery cannot be replayed later. A replay inside that minute finds the issue's
  existing job.
- **Size.** Bodies over 256 KiB are refused.
- **Trigger.** Only an issue from a connected team that was just assigned to the app starts
  a job. Other edits, including edits to an issue the app already has, do nothing.
- **Cap.** Past `max_jobs_per_hour`, Machinist answers `429` and logs it, and Linear retries
  later. Retries of an existing job do not count.

Anyone who can assign issues in a connected team can start a job, and the issue's text
becomes the prompt. `prompts/rules.md` tells the agent that the prompt cannot change its
rules. Connect only teams whose members you trust with that.

| Answer | Means |
| --- | --- |
| `201 {"job": "..."}` | A job started |
| `200 {"job": "..."}` | The issue already has this job |
| `200 {"ignored": "..."}` | Nothing to do, and why |
| `401` | Bad or missing signature, or a stale timestamp |
| `429` | The hourly cap is reached |
