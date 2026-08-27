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
the workflow. Create a fine-grained personal access token owned by a dedicated
user with write access to the repository and grant it read and write access to
issues. Store it as the `MACHINIST_INTAKE_TOKEN` Actions secret. The workflow
uses this token to check the comment author and apply the label, so the label
timeline names an actor whose write access Machinist can verify independently.
The normal `GITHUB_TOKEN` labels as `github-actions[bot]`, which is not a
repository collaborator and will not pass that check.

See [Managed triggers](../../docs/managed-triggers.md) for the control-plane
configuration and intake lifecycle.
