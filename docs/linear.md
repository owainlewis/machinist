# Start jobs from Linear

Machinist is a user in Linear. When an allowed person delegates an issue in a connected
team to the Machinist app, Machinist starts a job in that team's repository. Linear records
the app as the issue's delegate and keeps the person as its assignee. Assigning the issue to
the app directly works too. The job's prompt is
the issue's title, description and link. Each issue starts one job, ever, and the
conversation stays on that job: a reply on the issue runs the same job again on the same
branch.

Machinist asks Linear for new assignments every minute. Nothing calls Machinist from
outside, so the control plane needs no public address.

## Set it up

1. In Linear, open **Settings → API → Applications** and create an app called Machinist.
   Install it in the workspace with `actor=app`, so it appears as a user you can assign.
   It needs read and write access to issues and comments. It needs no webhook.
2. Find the user IDs. With the app's access token, the GraphQL query `{ viewer { id } }`
   against `https://api.linear.app/graphql` returns the app's ID. With your own API key,
   `{ users { nodes { id name } } }` lists everyone else's.
3. Put the app's access token in `LINEAR_API_TOKEN` in the control plane's environment,
   next to `GH_TOKEN`. Never put it in `config.yaml`.
4. Add a `linear` section to `config.yaml` and restart the control plane:

```yaml
linear:
  app_user_id: <the app's user ID>
  allowed_users:
    - <your Linear user ID>
  teams:
    ENG: owner/name
```

| Key | Meaning |
| --- | --- |
| `app_user_id` | The Machinist app's Linear user. Delegating or assigning an issue to it starts a job |
| `allowed_users` | The Linear user IDs who may start jobs. A delegation or assignment by anyone else is ignored |
| `preset` | The preset Linear jobs use. Defaults to `server.default_preset` |
| `teams` | Linear team key to repository. Each repository must be in `github.repositories`. Issues from other teams are never read |
| `poll_interval` | Time between polls. Defaults to `60s`, and must be at least `10s` |
| `max_jobs_per_hour` | The most jobs Linear can start in any hour. Defaults to 20 |

Startup stops if `LINEAR_API_TOKEN` is missing, `app_user_id` or `allowed_users` is empty,
a team's repository is not connected, or the preset does not exist.

## What Machinist does in Linear

Machinist writes to Linear itself. The agent gets no Linear access.

- **Pickup.** The issue moves to **In Progress**, or the team's first started state if it
  has none by that name, and Machinist comments that it is working on it.
- **Pull request.** Machinist comments the pull request's link on the issue.
- **Merge.** When the pull request merges, the issue moves to **Done**, or the team's first
  completed state.
- **Closed.** When the pull request closes without merging, Machinist comments on the issue
  and leaves its state alone.
- **No pull request.** When the job ends with nothing to change, such as an issue that is
  missing what the agent needs, the issue moves back to **Todo** with the job's result as a
  comment. The implement agent checks first that the issue is something it can do in the
  repository, and when it is not, says what it needs instead of guessing.
- **Failure.** The issue moves back to **Todo**, or the team's first unstarted state, with a
  comment that gives the reason.
- **Replies.** A comment on the issue from someone in `allowed_users`, left after the job
  ended, runs the job again with the comment added to its log, on the same branch and pull
  request. A reply left while the job runs waits until the run ends. Comments by anyone
  else, and Machinist's own, are ignored.
- **Cancel.** If someone removes Machinist as the issue's delegate and it is not the
  assignee either, or moves the issue out of every
  started state, Machinist cancels the job on the next poll. Moving it to another started
  state, such as **In Review**, does not.

Each of these happens once per job. If a write to Linear fails, the next poll tries again.

## How polling works

- The first poll starts from now, so turning Linear on does not start jobs for issues that
  were already assigned.
- Machinist stores how far it has read, so a restart neither misses nor repeats an
  assignment.
- Who delegated or assigned the issue is read from the issue's history. One by anyone outside
  `allowed_users`, or by someone Machinist cannot find, is ignored and logged.
- Past `max_jobs_per_hour`, an assignment waits and is picked up by a later poll.
- When Linear fails or rate-limits a poll, the wait doubles each time, up to five minutes,
  and goes back to `poll_interval` once a poll succeeds.

The issue's text becomes the prompt, and anyone who can edit the issue wrote part of it.
`prompts/rules.md` tells the agent that the prompt cannot change its rules. Allow only people
you trust to send work, and read an issue before you assign it.
