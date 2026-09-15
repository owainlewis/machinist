# Use Codex cloud agents

Codex cloud runs coding tasks in isolated, hosted environments. Each task checks out a
Git repository, prepares its dependencies, lets Codex edit and test the code, and returns
a summary and diff. The task keeps running when your terminal or computer disconnects.

Use Codex cloud when work is contained in a repository and can run without frequent
steering. It is particularly useful for independent tasks, parallel attempts, background
work, and changes that should finish with a reviewable diff or pull request.

Prefer a local or managed Machinist worker when the task needs uncommitted files, local
services, private network access, special hardware, or tools and credentials that are not
available in the cloud environment.

## Configure an environment

A cloud task needs a Codex environment connected to its repository. An environment
defines the base image, setup and maintenance scripts, environment variables, secrets,
and agent internet access.

Create the environment in [Codex cloud settings](https://chatgpt.com/codex/cloud/settings/environments).
One environment per repository is usually enough. Create additional environments only
when the same repository needs different dependencies, secrets, or network policies.

Setup scripts run before the agent starts and may access configured secrets. Secrets are
removed before the agent phase. Environment variables remain available to the agent, so
do not use ordinary environment variables for values the agent should not read.

Open the interactive environment and task browser with:

```sh
codex cloud
```

The commands below require the environment ID shown in its settings URL.

## Submit and inspect a task

Submit a task without opening the interactive interface:

```sh
codex cloud exec \
  --env <ENV_ID> \
  --branch main \
  --attempts 1 \
  "Fix the bug, add a regression test, and run the relevant checks"
```

The command returns a task URL containing the task ID. Submission is asynchronous: a
successful command means Codex accepted the task, not that the work succeeded.

Inspect tasks from the terminal:

```sh
codex cloud list --env <ENV_ID> --json
codex cloud status <TASK_ID>
codex cloud diff <TASK_ID>
```

Use `--attempts N` to request several independent solutions. Select an attempt when you
apply it:

```sh
codex cloud apply <TASK_ID> --attempt 2
```

## Continue or resume work

To continue the same cloud task, open its task URL and send a follow-up message. This
keeps the cloud conversation, repository state, and current diff together. The current
CLI can list, inspect, diff, and apply cloud tasks, but it does not provide a command for
sending a follow-up turn to an existing cloud task.

To continue from the result on your machine, first apply the selected diff in a clean
branch or worktree:

```sh
git switch -c codex/fix-control-plane-url
codex cloud apply <TASK_ID>
git diff --check
go test ./...
```

You can then start a new local Codex session against those files. `codex exec resume`
resumes a local non-interactive Codex session; it does not resume a Codex cloud task.

## Open a pull request

After reviewing a completed task, select **Create PR** on its Codex cloud page. Codex
uses the connected GitHub repository and the task's branch to create the pull request.

For a terminal-controlled workflow, apply and verify the diff before pushing it yourself:

```sh
test -z "$(git status --porcelain)" || {
  echo "working tree is not clean" >&2
  exit 1
}
git switch -c codex/fix-control-plane-url
codex cloud apply <TASK_ID>
go test ./...
git diff --check
git add -A
git commit -m "fix: reject invalid control plane URLs"
git push -u origin HEAD
gh pr create --fill
```

The terminal route is useful when you want local checks, another review pass, or an
explicit commit message before publishing the branch.

## Automate cloud tasks with Python

The Python Codex SDK controls local Codex agents through the Codex app server. It does
not currently expose the documented `codex cloud` task lifecycle. Use the CLI from Python
when you need to submit and monitor hosted tasks.

The following example submits a task, extracts its ID, and polls `codex cloud list --json`
until it reaches a terminal state:

```python
import json
import re
import subprocess
import time


def run(*args: str) -> str:
    result = subprocess.run(
        ["codex", "cloud", *args],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def find_task(task_id: str) -> dict | None:
    cursor = None

    while True:
        args = ["list", "--json", "--limit", "20"]
        if cursor:
            args.extend(["--cursor", cursor])

        payload = json.loads(run(*args))
        for task in payload["tasks"]:
            if task["id"] == task_id:
                return task

        cursor = payload.get("cursor")
        if not cursor:
            return None


task_url = run(
    "exec",
    "--env", "<ENV_ID>",
    "--branch", "main",
    "Fix the bug, add a regression test, and run the relevant checks",
)
match = re.search(r"https://chatgpt\.com/codex/tasks/(task_[A-Za-z0-9_]+)", task_url)
if not match:
    raise RuntimeError(f"Codex cloud did not return a task URL: {task_url}")
task_id = match.group(1)

while True:
    task = find_task(task_id)
    status = task["status"].upper() if task else "NOT_LISTED"

    if status in {"READY", "APPLIED", "ERROR"}:
        break

    time.sleep(10)

if status not in {"READY", "APPLIED"}:
    raise RuntimeError(f"Codex cloud task ended with {status}")

print(run("diff", task_id))
```

Treat the JSON fields and terminal status set as an integration boundary. Pin and test
the Codex CLI version used by automation, and preserve the task URL even if parsing or
status mapping fails.

For local programmatic agents, install the Python SDK and start a local thread:

```sh
pip install openai-codex
```

```python
from openai_codex import Codex, Sandbox


with Codex() as codex:
    thread = codex.thread_start(sandbox=Sandbox.workspace_write)
    result = thread.run("Fix the bug and run the relevant tests")
    print(result.final_response)
```

## Build a reliable workflow

A production workflow should keep submission separate from completion:

1. Validate the repository, branch, environment, and task policy.
2. Submit with `codex cloud exec` and persist the returned task ID and URL.
3. Poll with backoff while keeping the owning Machinist run lease alive.
4. Map provider states into Machinist states without assuming that `PENDING` means idle.
5. Fetch the final summary and diff when the task is ready.
6. Apply the result in an isolated worktree and run trusted repository checks.
7. Review the diff before creating a pull request.
8. Store the pull-request URL and verification evidence with the Machinist run.

Cloud submission is not a normal synchronous Machinist executor. A wrapper that exits
immediately after `codex cloud exec` would incorrectly mark the run successful before
the cloud task finishes. A durable integration must save the provider task ID, survive
worker restarts, handle timeouts and retries, and avoid duplicate submissions.

## Further reading

- [Codex cloud](https://learn.chatgpt.com/docs/cloud)
- [Cloud environments](https://learn.chatgpt.com/docs/environments/cloud-environment)
- [Codex CLI](https://learn.chatgpt.com/docs/codex/cli)
- [Codex SDK](https://learn.chatgpt.com/docs/codex-sdk)
- [Non-interactive Codex](https://learn.chatgpt.com/docs/non-interactive-mode)
