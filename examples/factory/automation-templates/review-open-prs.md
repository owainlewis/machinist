+++
schedule = "0 9 * * 1-5"
timezone = "UTC"
repo = "owner/name"
agent = "maintainer"
+++
Review open pull requests in this repository, and merge the ones that are ready.

A pull request is ready when all of these are true:

- It is not a draft.
- Every check has passed.
- No one has an open request for changes.
- It does what its description says, and you found no bug in its diff.

Merge each ready pull request with `gh pr merge --squash`. Leave the rest alone.
Report which you merged, which you left, and why.
