# Start jobs from Linear

Machinist is a user in Linear. When an allowed person delegates an issue to the Machinist
app, Machinist starts a job in the repository that the issue's project or team maps to. Linear records
the app as the issue's delegate and keeps the person as its assignee. Assigning the issue to
the app directly works too. The job's prompt is
the issue's title, description and link. Each issue starts one job, ever, and the
conversation stays on that job: a reply on the issue runs the same job again on the same
branch.

Machinist asks Linear for new assignments every minute. Nothing calls Machinist from
outside, so the control plane needs no public address.

## Set it up

This takes about ten minutes. You need to be a Linear workspace admin, and able to restart
the control plane.

Machinist signs in to Linear as its own app, with the app's client ID and secret. It gets an
access token with them when it starts, and gets a new one before that token expires, so
nobody has to renew anything by hand. Linear's tokens last 30 days.

### 1. Make the Linear app

In Linear, open **Settings → API → Applications** and choose **New application**. Fill in
the form:

| Field | Value |
| --- | --- |
| Application name | `Machinist`. People see this name when they assign it an issue |
| Developer name | Your name or your team's |
| Developer URL | Any page about your Machinist, such as your repository's URL |
| Redirect URIs | Leave blank. Machinist does not use the OAuth redirect flow |
| Public | Off. Only your workspace uses the app |
| Client credentials | On. This is how Machinist gets its own tokens |
| Webhooks | Off. Machinist polls Linear, so nothing calls it from outside |

Save the app. Linear shows its **Client ID** and **Client secret**. Copy both now.

### 2. Give the control plane the client ID and secret

Set `LINEAR_CLIENT_ID` and `LINEAR_CLIENT_SECRET` where the control plane runs. They never
go in `config.yaml`, which can live in git; startup fails if you put them there.

- **On a VM**, add them to `/etc/machinist/control-plane.env`, next to `GH_TOKEN`. The
  systemd unit reads that file. See [VM deployment](vm-deployment.md).

  ```sh
  LINEAR_CLIENT_ID=<the client ID>
  LINEAR_CLIENT_SECRET=<the client secret>
  ```

- **On your own machine**, put the same two lines in a `.env` file in the directory you run
  `machinist start` from. Add `.env` to that directory's `.gitignore` before you write the
  secret into it. Machinist does not read `.env` itself, so load it into the shell first:

  ```sh
  echo .env >> .gitignore
  set -a; . ./.env; set +a
  machinist start
  ```

Treat the secret like a password. Anyone with it can act as Machinist in your Linear
workspace.

### 3. Find the user IDs

Machinist needs two kinds of Linear user ID: the app's own, and the IDs of the people
allowed to send it work.

**The app's ID.** Ask Linear for a token with the client credentials, then ask who that
token is:

```sh
token=$(curl -s https://api.linear.app/oauth/token \
  -d grant_type=client_credentials -d scope=read,write,app:assignable,app:mentionable \
  -d client_id="$LINEAR_CLIENT_ID" -d client_secret="$LINEAR_CLIENT_SECRET" \
  | python3 -c 'import json, sys; print(json.load(sys.stdin)["access_token"])')
curl -s https://api.linear.app/graphql \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"query": "{ viewer { id name } }"}'
```

The `id` it returns is `app_user_id`.

Ask for all four scopes. Without `app:assignable` and `app:mentionable`, Linear refuses to
let anyone delegate or assign an issue to the app.

Getting a token this way revokes the app's previous token. If a control plane is already
running with these credentials, it gets a new one on its next poll, but a second control
plane sharing the same client ID and secret will keep taking the token from the first. Give
each control plane its own Linear app.

**People's IDs.** Use the same token to list everyone in the workspace:

```sh
curl -s https://api.linear.app/graphql \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"query": "{ users { nodes { id name email } } }"}'
```

Pick out the `id` of each person who may send Machinist work. These go in `allowed_users`.

### 4. Add the `linear` section

Add a `linear` section to `config.yaml`. Each repository it names must be connected: in
`github.repositories`, or added in **Settings → Projects**.

```yaml
linear:
  app_user_id: <the app's user ID>
  allowed_users:
    - <your Linear user ID>
  projects:
    Machinist: owner/machinist
    Airtime: owner/airtime
  teams:
    ENG: owner/name
```

| Key | Meaning |
| --- | --- |
| `app_user_id` | The Machinist app's Linear user. Delegating or assigning an issue to it starts a job |
| `allowed_users` | The Linear user IDs who may start jobs. A delegation or assignment by anyone else is ignored |
| `preset` | The preset Linear jobs use. Defaults to `server.default_preset` |
| `projects` | Linear project, by name or ID, to repository. Names match in any case. Each repository must be in `github.repositories` or **Settings → Projects** |
| `teams` | Linear team key to repository, for issues whose project is not in `projects`. Each repository must be in `github.repositories` or **Settings → Projects** |
| `poll_interval` | Time between polls. Defaults to `60s`, and must be at least `10s` |
| `max_jobs_per_hour` | The most jobs Linear can start in any hour. Defaults to 20 |

Set `projects`, `teams` or both. Startup stops if only one of `LINEAR_CLIENT_ID` and
`LINEAR_CLIENT_SECRET` is set, if no credentials are set, if `app_user_id` or
`allowed_users` is empty, if a project's or team's repository is not connected, or if the
preset does not exist.

### 5. Check that it works

1. Restart the control plane. Open **Settings → Linear** in the web app. It should say
   **On** and list your teams.
2. In Linear, make a small test issue in a connected team, such as "Add a sentence to the
   README saying hello". Delegate or assign it to Machinist.
3. Within a minute, the issue moves to **In Progress** and Machinist comments that it is
   working on it. The job shows on the web app's Jobs page.

If nothing happens, read the control plane's log. A wrong client ID or secret shows as
`linear: get an access token from the client credentials: ...` on each poll. An issue
assigned by someone outside `allowed_users` is logged and ignored.

### Renewing the token

Machinist keeps the token in memory, and gets a new one an hour before it expires and each
time it restarts. If Linear turns a token down before then, with a 401, Machinist gets a
new one once and sends the request again. Nothing is written to disk.

### Using a fixed token instead

`LINEAR_API_TOKEN` still works. Set it to an access token, or to a personal API key that
starts with `lin_api_`, in place of the client ID and secret. Machinist uses it as it is and
cannot renew it, so when it expires polling stops until you set a new one. If both are set,
the client ID and secret win.

## Which repository a job runs in

1. The issue's project, if `projects` names it by ID or by name.
2. Otherwise the issue's team, if `teams` names it.
3. Otherwise no job starts. Machinist logs why and comments on the issue that its project
   and team map to no repository. It comments once each time the issue is handed to it, not
   on every edit.

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
