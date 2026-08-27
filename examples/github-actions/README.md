# GitHub comment intake

Copy [`machinist-comment-intake.yml`](machinist-comment-intake.yml) to
`.github/workflows/machinist-comment-intake.yml` in a repository that a
Machinist GitHub trigger watches.

The workflow handles only new issue comments. It ignores pull requests, edited
comments, comments whose first non-empty line does not start with `@machinist`,
and comments from users without write, maintain, or admin access. An accepted
comment adds `machinist:requested`. It does not check out or run repository
code.

Create the `machinist:requested` and `machinist:queued` labels before enabling
the workflow. The normal `GITHUB_TOKEN` is enough for repositories that allow
GitHub Actions to write issues. If the repository limits that token to read-only
access, change the repository's Actions workflow permissions before using this
example.

See [Managed triggers](../../docs/managed-triggers.md) for the control-plane
configuration and intake lifecycle.
