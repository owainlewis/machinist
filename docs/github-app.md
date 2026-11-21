# GitHub App

With a GitHub App, no long-lived GitHub token sits on the control plane or the runners.
The control plane holds the app's private key. For each run it makes a new token that
reaches the job's repository and nothing else, and that GitHub stops accepting after one
hour. The runner hands that token to the steps in place of its own `GH_TOKEN`.

A prompt injection in one repository can then reach only that repository, and only for the
rest of that run.

## What a run's token can do

Machinist asks GitHub for exactly these permissions on every token
(`internal/githubapp/app.go`). The app must have all of them, or GitHub refuses the token
and the job fails.

| Permission | Access | Used for |
| --- | --- | --- |
| Contents | Read and write | Clone and push the job's branch |
| Pull requests | Read and write | Open and update the draft pull request, and track it after the run |
| Issues | Read and write | Read issues and post comments |
| Checks | Read | Read CI results on the pull request |
| Commit statuses | Read | Read CI results that use statuses |
| Metadata | Read | Always granted by GitHub |

It cannot change workflow files, settings, secrets, branch protection, or any other
repository. A push that changes `.github/workflows/` is refused, because the app has no
Workflows permission.

The control plane's own GitHub calls, such as finding a job's pull request and posting
comments, use tokens from the same app with the same permissions. It reuses each one until
ten minutes before it expires.

## Set it up

This takes about ten minutes. Sign in to GitHub as the account that owns the
repositories, or as an owner of their organization.

### 1. Create the app

Open this link. It fills in the form with no webhook, the permissions above and private
installation:

```text
https://github.com/settings/apps/new?name=machinist-yourname&url=https://github.com/owainlewis/machinist&webhook_active=false&public=false&contents=write&pull_requests=write&issues=write&checks=read&statuses=read
```

Change `machinist-yourname` to a name of your own. App names are unique across GitHub, and
pull requests and comments show it as `machinist-yourname[bot]`. For an organization, start
from **Settings > Developer settings > GitHub Apps > New GitHub App** in the organization
and fill in the same values by hand.

Before you create it, check the form, because GitHub does not always apply every value
from the link:

- **Webhook**: **Active** is cleared. Machinist does not use app webhooks, so nothing has
  to reach your host from the internet.
- **Repository permissions**: exactly the six in the table above. Every other permission,
  and every organization and account permission, is **No access**.
- **Where can this GitHub App be installed?**: **Only on this account**.

Select **Create GitHub App**. Note the **App ID** at the top of the next page.

### 2. Make a private key

On the app's page, under **Private keys**, select **Generate a private key**. Your browser
downloads a `.pem` file. Put it next to `config.yaml`, inside `.machinist/`, which git
ignores, and make it readable only by the user that runs the control plane:

```sh
mv ~/Downloads/machinist-yourname.*.private-key.pem .machinist/github-app.pem
chmod 600 .machinist/github-app.pem
```

For a [VM](vm-deployment.md), copy it over, hand it to the `machinist` user, then delete
the downloaded copy:

```sh
scp ~/Downloads/machinist-yourname.*.private-key.pem VM_HOST:/home/machinist/.machinist/github-app.pem
ssh VM_HOST 'chown machinist:machinist /home/machinist/.machinist/github-app.pem && chmod 600 /home/machinist/.machinist/github-app.pem'
rm ~/Downloads/machinist-yourname.*.private-key.pem
```

### 3. Install it on your repositories

Select **Install App** in the app's sidebar, then **Install** next to your account. Choose
**Only select repositories**, pick the repositories Machinist works on, and select
**Install**. Do not choose **All repositories**: anyone with the key can make tokens for
every repository the app is installed on.

### 4. Tell Machinist

Add the app to `config.yaml`:

```yaml
github:
  handle: machinist-bot
  repositories: [owner/name]
  app:
    id: 123456
    private_key_file: .machinist/github-app.pem
```

Every repository in `repositories` must be one the app is installed on.

Restart the control plane. It reads the key at startup and stops with an error if it
cannot use it. On a VM:

```sh
systemctl restart machinist-control-plane machinist-runner
```

Runs commit as the app's bot account, such as `machinist-yourname[bot]`, so GitHub shows
the app as the author of every commit, pull request, comment and merge. Machinist looks the
account up from the app the first time a run starts.

Runners no longer need `GH_TOKEN`. A runner that still has one uses it only for a run that
arrives without a token, and for `machinist runner check`, which runs without the control
plane.

### 5. Check it works

Open the **Setup** page in the web app. It shows the app's bot account and flags any
project the app is not installed on. Add more projects there, with no restart. Each one
needs the app installed on it first.

Then start a job on one of the repositories, from the web app's **New job** form or the
[run API](run-api.md). If the checkout step succeeds, the app works. If the job fails before
any step runs, its page says why:

| Message | Fix |
| --- | --- |
| `the GitHub App is not installed on owner/name` | Install the app on that repository, as in step 3 |
| `GitHub replied 422: The permissions requested are not granted to this installation` | The app or its installation is missing a permission. See below |

## Change the app's permissions

Changing a permission on the app does not change existing installations. GitHub asks the
account that installed the app to accept the change first. Until then, tokens keep the old
permissions, and Machinist's request fails with the `422` above.

1.  Open the app's settings, then **Permissions & events**. Set the permissions in the
    table above and select **Save changes**.
2.  Open [Installed GitHub Apps](https://github.com/settings/installations), or the
    organization's **Settings > GitHub Apps**, and select **Configure** next to the app.
3.  Accept the new permissions in the banner at the top of the page.

## Limits

- A token lasts one hour. A local runner asks the control plane for a new one before each
  step, for the same repository, so a late step such as `merge` starts with a full hour.
  Kubernetes runs keep the token they started with. Either way, `run_timeout` and every
  preset's `timeout` must be one hour or less. Startup stops and names the setting otherwise.
- The key is the one long-lived secret left. Anyone who has it can make tokens for every
  repository the app is installed on. Keep it readable only by the control plane's user.
  If it leaks, generate a new one in the app's settings and delete the old one.
