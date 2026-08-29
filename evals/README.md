# Local evals

The first Machinist eval exercises the complete default workflow and checks its issue-label
lifecycle. It is intentionally small. It does not judge implementation quality.

The eval creates a disposable issue and allows the `foreman` agent to create a pull
request. After capturing the issue events, it closes the pull request and issue, deletes
the remote branch, and removes the generated local worktree when it is clean. GitHub does
not allow pull requests or issues to be deleted, so the closed resources remain in the
scratch repository's history.

Use a dedicated scratch repository with a clean local checkout. The GitHub CLI must be
authenticated, and the Machinist configuration must define the default `foreman` agent.

```sh
just build

python3 -m evals.github_labels \
  --repository=your-org/machinist-evals \
  --repo-path=/absolute/path/to/machinist-evals \
  --machinist=./bin/machinist
```

Optional `--worker-config`, `--machinist-config`, and `--model` arguments select non-default
Machinist configuration. The command prints agent output while it runs and exits non-zero
when Machinist fails, the label lifecycle is wrong, or cleanup is incomplete.

Run the local, non-mutating tests with:

```sh
python3 -m unittest discover -s evals -p 'test_*.py'
```

## Shepherd queue smoke test

The Shepherd eval creates a temporary base branch and disposable pull requests in a
dedicated scratch repository. It proves unlabelled pull requests are unchanged, an older
labelled draft does not block an eligible merge, and an action-limited candidate is
deferred. It then exercises a parent and child stack across separate `max_actions=1` runs:
the transition is recorded, the parent merges, the persisted transition is recovered, the
child is retargeted, and a later run merges the newly verified child. Each GitHub mutation,
including the audit updates between those steps, consumes the single action available in
its run. The smoke rejects review evidence from another base branch or base SHA even when
the child head is unchanged, rejects review markers not provably written by an independent
authenticated Shepherd actor, and rejects stack markers without that trusted provenance or
exact ref and SHA facts. It also rejects transition evidence created after the parent
merged or edited at any time, and requires a later retarget record to reference the immutable pending
comment ID. A first run proves Shepherd creates the missing
`machinist:auto-merge` label definition. Cleanup
closes open pull requests and deletes the temporary branches and label, so the repository's
default branch is unchanged.

The repository must start without the auto-merge label. Repeat its name with
`--confirm-disposable` to acknowledge that the run creates and merges disposable pull
requests. Use a Machinist definition without a configured Shepherd schedule so the
disposable direct smoke cannot overlap production scheduled work. Set a token for a
second GitHub account that can create pull requests in the scratch repository. The eval
uses that account for candidates so Shepherd's authenticated account provides an
independent review:

```sh
just build

python3 -m evals.shepherd_queue \
  --repository=your-org/machinist-evals \
  --confirm-disposable=your-org/machinist-evals \
  --repo-path=/absolute/path/to/machinist-evals \
  --candidate-token-env=MACHINIST_EVAL_CANDIDATE_TOKEN \
  --machinist=./bin/machinist
```
