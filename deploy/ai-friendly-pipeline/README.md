# AI Friendly pipeline: assignment-app tickets, driven by `machinist run`

This is the config for a **standalone** `machinist run` invocation, not a
control-plane/worker deployment. It backs a scheduled Azure Pipeline in the
`assignment-app` repo that polls Linear for issues labelled `AI Friendly` and
drives each one through the `assignment` Claude Code plugin's
`/assignment:start` flow to a real pull request, with no human running Claude
by hand. See that repo's `scripts/ai-friendly-poll.mjs` and
`azure-pipelines-ai-friendly.yml` for the trigger/label-swap side.

## Why standalone, not a Machinist service

`machinist run` needs only `worker.toml` (executors) + `config.toml`
(commands) - no control plane, no store, no worker daemon. The Azure
Pipeline's own cron schedule plays the role of Machinist's poll-based
triggers (`internal/controlplane/trigger_scheduler.go`), so we don't need to
stand up `machinist start` / `machinist worker start` as long-running
services just for this.

## No machine access required

`azure-pipelines-ai-friendly.yml` (in assignment-app) is fully self-contained:
it `go install`s a pinned `machinist` release, checks out assignment-app
itself, and writes `worker.toml`/`config.toml` (copies of the two files in
this directory) into the job's temp dir - all as ordinary pipeline steps.
Nothing needs to be pre-installed or kept in sync on the agent host by hand,
so you don't need shell/SSH access to the `QOCOManagedPool` box to set this
up, only permission to author/run pipelines in Azure DevOps.

What you do still need to provision, as Azure DevOps **secret pipeline
variables** (Pipeline > Edit > Variables, marked secret - no machine access
needed for this either):

- **`CLAUDE_CODE_OAUTH_TOKEN`** - this runs against the org's Claude
  subscription, not a pay-per-token API key. Whoever holds a Pro/Max/Team/
  Enterprise seat runs `claude setup-token` **once, on their own machine**
  (opens a normal browser login, no server access involved), then pastes the
  printed token into this pipeline variable. It's a long-lived token (~1
  year); rotate it the same way when it expires.
- **`LINEAR_API_KEY`** - a Linear personal or workspace API key with access
  to the Assignment team.
- **`AZURE_DEVOPS_PAT`** - for `assignment:pull-request`'s `create-pr` step
  (`az repos pr create`). Must be a **service identity's** PAT, not a
  developer's, since this runs unattended and needs to keep working
  independent of any one person's account.

## Manual smoke test before enabling the schedule

Run the same steps the pipeline runs, once, by hand (locally or in a manual
pipeline run) before turning on the `*/10 * * * *` schedule:

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...   # from `claude setup-token`
machinist run --command=assignment-start \
  --repo=/path/to/a/throwaway/assignment-app/checkout/codebase \
  --machinist-config=deploy/ai-friendly-pipeline/config.toml \
  --config=deploy/ai-friendly-pipeline/worker.toml \
  --prompt="/assignment:start ASN-<a trivial real ticket>" \
  --model=sonnet
```

Confirm the exit code (0 = success) and the events log path Machinist prints
look right, and that a real PR gets opened, before wiring this into the
scheduled pipeline.
