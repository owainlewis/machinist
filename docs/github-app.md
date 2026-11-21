# GitHub App

With a GitHub App, no long-lived GitHub token sits on the control plane or the runners.
The control plane holds the app's private key. For each run it makes a new token that
reaches the job's repository and nothing else, and that GitHub stops accepting after one
hour. The runner hands that token to the steps in place of its own `GH_TOKEN`.

A prompt injection in one repository can then reach only that repository, and only for the
rest of that run.

## What a run's token can do

| Permission | Access | Used for |
| --- | --- | --- |
| Contents | Read and write | Clone and push the job's branch |
| Pull requests | Read and write | Open, update and merge the pull request |
| Issues | Read and write | Read issues and post comments |
| Checks | Read | Read CI results on the pull request |
| Commit statuses | Read | Read CI results that use statuses |
| Metadata | Read | Always granted by GitHub |

It cannot change workflow files, settings, secrets, branch protection, or any other
repository. A push that changes `.github/workflows/` is refused.

The control plane's own GitHub calls, such as finding a job's pull request and posting
comments, use tokens from the same app with the same permissions. It reuses each one until
ten minutes before it expires.

## Set it up

1.  Open [New GitHub App](https://github.com/settings/apps/new). For an organization, use
    the organization's **Settings > Developer settings > GitHub Apps > New GitHub App**.
2.  Fill in:
    - **GitHub App name**: a name such as `machinist-yourname`. Comments and pull requests
      show it as `machinist-yourname[bot]`.
    - **Homepage URL**: any URL, such as your Machinist repository.
    - **Webhook**: clear **Active**. Machinist does not use app webhooks.
3.  Under **Repository permissions**, set exactly the permissions in the table above.
    Leave every other permission at **No access**.
4.  Under **Where can this GitHub App be installed?**, choose **Only on this account**.
5.  Select **Create GitHub App**. Note the **App ID** at the top of the next page.
6.  Under **Private keys**, select **Generate a private key**. Your browser downloads a
    `.pem` file. Move it next to `config.yaml`, inside `.machinist/`, which git ignores:

    ```sh
    mv ~/Downloads/machinist-yourname.*.private-key.pem .machinist/github-app.pem
    chmod 600 .machinist/github-app.pem
    ```

7.  Select **Install App** in the sidebar, then **Install** next to your account. Choose
    **Only select repositories** and pick the repositories Machinist works on.
8.  Add the app to `config.yaml`:

    ```yaml
    github:
      handle: machinist-bot
      repositories: [owner/name]
      app:
        id: 123456
        private_key_file: .machinist/github-app.pem
    ```

9.  Restart the control plane. It reads the key at startup and stops with an error if it
    cannot use it.

Runs also commit as the app's bot account, such as `machinistagent[bot]`, so GitHub shows
the app as the author of every commit, pull request, comment and merge. Machinist looks the
account up from the app the first time a run starts.

Runners no longer need `GH_TOKEN`. A runner that still has one uses it only for a run that
arrives without a token, and for `machinist runner check`, which runs without the control
plane.

## Limits

- A token lasts one hour, so with an app `run_timeout` and every preset's `timeout` must be
  one hour or less. Startup stops and names the setting otherwise.
- If the app is not installed on a job's repository, the job fails with a message that says
  so. Install the app on that repository and run the job again.
- The key is the one long-lived secret left. Anyone who has it can make tokens for every
  repository the app is installed on. Keep it readable only by the control plane's user,
  and generate a new one in the app's settings if it leaks.
