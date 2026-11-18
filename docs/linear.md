# Start jobs from Linear

Machinist starts a job when an issue in a connected Linear team gets the trigger label, or
is assigned to the Machinist app. The job's prompt is the issue's title, description and
link. Each issue starts one job, ever: adding the label again, or Linear retrying a
delivery, returns the first job.

## Set it up

1. In Linear, open **Settings → API → Webhooks** and add a webhook. To assign issues to
   Machinist, create an app under **Settings → API → Applications** instead, switch on its
   webhook and install it.
   - URL: `https://<your host>/webhooks/linear`
   - Resource: **Issues**
2. Copy the webhook's signing secret into `LINEAR_WEBHOOK_SECRET` in the control plane's
   environment, next to `GH_TOKEN`. Never put it in `config.yaml`.
3. Add a `linear` section to `config.yaml` and restart the control plane:

```yaml
linear:
  trigger_label: machinist
  # app_user_id: <the app's Linear user ID>
  preset: coding
  teams:
    ENG: owner/name
  max_jobs_per_hour: 20
```

| Key | Meaning |
| --- | --- |
| `trigger_label` | Adding this label to an issue starts a job. Case does not matter |
| `app_user_id` | Assigning an issue to this Linear user starts a job |
| `preset` | The preset Linear jobs use. Defaults to `server.default_preset` |
| `teams` | Linear team key to repository. Each repository must be in `github.repositories`. Issues from other teams are ignored |
| `max_jobs_per_hour` | The most jobs Linear can start in any hour. Defaults to 20 |

Set at least one of `trigger_label` and `app_user_id`. Startup stops if the secret is
missing or shorter than 16 characters, a team's repository is not connected, or the preset
does not exist.

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
- **Size.** Bodies over 256 KiB are refused before they are read.
- **Triggers.** Only an issue being created or updated, from a connected team, that just
  got the label or was just assigned to the app, starts a job. Edits to an issue that
  already has the label do nothing.
- **Cap.** Past `max_jobs_per_hour`, Machinist answers `429` and logs it, and Linear retries
  later. Retries of an existing job do not count.

Anyone who can label or assign issues in a connected team can start a job, and the issue's
text becomes the prompt. `prompts/rules.md` tells the agent that the prompt cannot change
its rules. Connect only teams whose members you trust with that.

| Answer | Means |
| --- | --- |
| `201 {"job": "..."}` | A job started |
| `200 {"job": "..."}` | The issue already has this job |
| `200 {"ignored": "..."}` | Nothing to do, and why |
| `401` | Bad or missing signature, or a stale timestamp |
| `429` | The hourly cap is reached |
