# Prompt to PR in gVisor

One prompt starts a disposable Docker container using `runsc`. Inside it, the worker
clones the target repository's current `main`, creates a unique `codex/task-*`
branch, runs Codex, commits the changes, pushes the branch, and opens a PR for human
review. It does not merge or enable auto-merge. No checkout, home directory, or
Docker socket is mounted from the host.

## Setup

The host needs Python 3.10+, Docker, and the
[gVisor runsc runtime](https://gvisor.dev/docs/user_guide/quick_start/docker/).
The launcher fails if `runsc` is unavailable; it never falls back to `runc`.

Build from the Machinist repository root:

```sh
docker build -t machinist-gvisor-pr:local examples/workflows/gvisor-pr
```

The image includes pinned Codex, Node, and Go versions, plus Git, GitHub CLI,
Python, and C/C++ build tools. Add other repository dependencies to the image.
The agent runs without root and cannot install system packages at runtime.
OS packages resolve at image build time; use an image digest for reproducible
worker deployments. Override `CODEX_VERSION` with a Docker build argument to upgrade.

Provide these environment variables through your secret manager or worker service:

- `CODEX_API_KEY`: an OpenAI API key for non-interactive Codex.
- `GH_TOKEN`: a GitHub token scoped to the target repository with Contents and
  Pull requests write access. Changes to workflow files may require additional
  permission. The token must allow creating branches in the target repository.

This example supports GitHub.com repositories with a `main` branch. It uses API
key authentication; it does not copy your local Codex account login.

## Run

```sh
printf '%s\n' 'Fix the empty-input bug and add a regression test.' |
  python3 examples/workflows/gvisor-pr/run.py --repo OWNER/REPO
```

Or supply a longer task file:

```sh
python3 examples/workflows/gvisor-pr/run.py \
  --repo OWNER/REPO --timeout 2700 < task.md
```

`--model MODEL_ID` selects a Codex model; otherwise Codex uses its default.
`--image IMAGE` selects a custom worker image. The container has 2 CPUs, 6 GiB
memory, and 512 processes. Its checkout and home live on a 4 GiB tmpfs; `/tmp`
has a separate 1 GiB tmpfs. These consume the container's memory budget. Adjust
the limits in `run.py` for larger repositories.

Logs stream to stdout/stderr. The last successful message contains the PR URL.
The agent supplies the PR title, description, and validation report. Publication
requires a `complete` report and a nonempty change; failures, missing reports,
changed HEAD/branch, and `blocked` reports fail without wrapper publication.
The completion report is an agent assessment, not an independent test gate.

## Machinist executor

In `worker.toml`, use an absolute path and a fixed target repository:

```toml
[executors.gvisor-pr]
command = ["python3", "/absolute/path/to/machinist/examples/workflows/gvisor-pr/run.py", "--repo", "OWNER/REPO", "--timeout", "2700"]
```

In `config.toml`:

```toml
[commands.gvisor-pr]
executor = "gvisor-pr"
timeout = "46m"
```

The longer outer timeout leaves time for container cleanup. The Machinist worker
must receive both credential variables. Its selected working directory is not
mounted or modified; the executor's fixed `--repo` determines the remote checkout.

## Cleanup and failure behavior

The launcher removes the container on completion, failure, its 45-minute default
deadline, SIGINT, and SIGTERM. Exit code 124 means timeout. Cleanup failures are
reported with the container name. Container removal discards the clone, temporary
files, and local credentials. The reusable image and published branch/PR remain.
If PR creation fails after a push, the logged branch remains on GitHub for recovery.
Rerunning always starts a fresh branch; it does not resume a previous run.

SIGKILL, host crashes, or an unavailable Docker daemon cannot guarantee immediate
cleanup. Containers carry the label `machinist.workflow=gvisor-pr`; inspect these
after recovery and remove confirmed orphaned runs explicitly:

```sh
docker ps -a --filter label=machinist.workflow=gvisor-pr
docker rm -f CONTAINER_NAME
```

## Isolation boundary

gVisor isolates code execution from the host. This example allows outbound network
access and passes both credentials into the sandbox. Agent commands and repository
tests may access those credentials. Use a narrowly scoped, short-lived GitHub token.
Prompt instructions about publishing are not an authorization boundary. For
untrusted repositories or multi-tenant hosting, move API authentication and GitHub
publishing behind external services and enforce outbound network policy.

## Tests

```sh
python3 -m unittest discover -s examples/workflows/gvisor-pr -p 'test_*.py'
```

The tests use real local Git repositories and simulated Codex/GitHub responses.
They cover publication, blocked/no-op/failed tasks, PR failure recovery, runtime
enforcement, exit status, timeout, cancellation, and cleanup failures. They make
no model API calls and open no real PRs.
