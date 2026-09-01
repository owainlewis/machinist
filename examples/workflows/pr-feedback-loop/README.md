# Pull request feedback loop

One script takes a task from implementation to a pull request that has answered its
reviewers. The script owns git and GitHub. The agent owns the code.

1. Create a branch and ask the agent to implement the task, have a fresh subagent review
   the diff, run the tests, and commit.
2. Push and open the pull request with `gh pr create --fill`.
3. Wait for feedback. Feedback is anything newer than the last push: an unresolved review
   thread with a new comment, a review that requests changes, or a failing check on the
   current head.
4. Hand the feedback to the agent, which fixes it, replies in each thread, and commits.
5. Push and go back to step 3, at most `MACHINIST_MAX_ROUNDS` times.

The loop ends with exit 0 when the pull request is approved with green checks, or when no
new feedback arrives within `MACHINIST_FEEDBACK_WAIT` seconds, or when the agent decides
nothing needs to change. It ends with exit 1 when feedback is still open after the last
round. The last push is the only cursor, so the script keeps no state on disk and a rerun
starts from a fresh branch.

## Set up

Copy `pr_feedback_loop.py` into the repository Machinist will run, for example as
`scripts/pr_feedback_loop.py`, and add the executor to `worker.toml`:

```toml
[executors.pr-feedback-loop-script]
command = ["python3", "./scripts/pr_feedback_loop.py"]
```

You need Python 3.10 or newer, and authenticated `git`, `gh`, and `codex` commands. Set
`MACHINIST_AGENT_COMMAND` to use a different agent, for example
`claude -p --output-format stream-json --verbose`.

## Run it

```sh
machinist run \
  --machinist-config=/path/to/machinist/examples/workflows/pr-feedback-loop/config.toml \
  --command=pr-feedback-loop \
  --repo=/path/to/repo \
  --prompt="Add a --json flag to the status command"
```

Or queue it through the control plane with `machinist submit`, or select it from a trigger.

## Limits

Machinist sees only the script's output and exit code. A timeout or cancellation kills the
script and the agent underneath it. The script never resolves review threads and never
merges; those stay with people.
