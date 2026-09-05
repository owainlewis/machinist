# Flow: task to pull request

A small Python script does three things:

1. Fetch `origin/main` and create a `codex/` branch in a new worktree.
2. Run Codex in that worktree to implement the task, run local checks, and get a
   fresh subagent review. The builder fixes findings, with at most three repair rounds.
3. Push the reviewed commit, open a PR, print its URL, and exit.

A worktree is another checkout of the same Git repository with its own branch and
files. Your original checkout, including any uncommitted work, stays as it is.
The new checkout starts from remote `main`; it does not include local changes.
The script passes its path as Codex's working directory.

## Run it

The machine needs `uv`, Git, an authenticated `gh`, and Codex login credentials.
The pinned Python SDK supplies the Codex binary. Run from the target repository:

```sh
printf '%s\n' 'Add a --json flag to the status command' |
  uv run --script /absolute/path/to/flow.py
```

The repository must have an `origin` remote on GitHub and a `main` branch.
Worktrees are created under `~/Code/.worktrees/<repo>/<task>-<id>`. Set
`FLOW_WORKTREE_ROOT` to use another parent directory.

Codex uses `full-access` by default, allowing dependency installation and Git
writes to the shared repository metadata. A worktree isolates edits, not process
permissions. Set `FLOW_SANDBOX=workspace-write` if your Codex configuration grants
access to the worktree's shared Git metadata and any network access checks need.
Subagent review must be available in the selected Codex configuration; otherwise
the agent is instructed to report blocked.

## Run through Machinist

Copy `flow.py` to your repository, for example as `scripts/flow.py`, and register
it in `worker.toml`:

```toml
[executors.flow]
command = ["uv", "run", "--script", "./scripts/flow.py"]
```

Use the adjacent `config.toml` for the command definition:

```sh
machinist run \
  --machinist-config=/absolute/path/to/examples/workflows/flow/config.toml \
  --command=flow \
  --repo=/absolute/path/to/repo \
  --prompt="Add a --json flag to the status command"
```

The script reports SDK token usage through `MACHINIST_TOKEN_USAGE_PATH` when set.
Machinist owns the overall timeout and cancellation.

## Where it stops

Exit 0 means the PR was opened. The script does not wait for CI, answer GitHub
reviews, or merge. Those are separate work after this first step.

Before publishing, Python requires a clean worktree on the expected branch, a
change descended from the fetched base, and an approved report for the exact
commit being pushed. Tests and independent review are performed by the coding
agent and its subagent. Their report is agent-provided evidence; Python does not
independently prove that the reported checks or review happened.

The worktree is kept on success or failure, and its path is printed before coding
starts. Inspect failed work there. Each invocation creates new work; rerunning
is not a resume and can create another PR. If pushing succeeds but PR creation
fails, the branch remains available to open a PR manually. After the PR is merged
or closed, remove its clean worktree with `git worktree remove <path>`.
