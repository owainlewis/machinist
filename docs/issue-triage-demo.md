# Luna issue triage demo

The `Luna issue triage` GitHub Action runs when the repository owner opens an
issue. It uses Codex with `gpt-6-luna` to choose `bug`, `enhancement`,
`documentation`, or `needs-human`. Issues from other authors are skipped for
this public-repository demo. No Machinist server or worker is involved.

## Setup and demo

1. Add `OPENAI_API_KEY` in repository Settings → Secrets and variables → Actions.
   Use a Platform API key with access to `gpt-6-luna`; usage is billed to that
   API project, separately from a ChatGPT subscription. Never commit the key.
2. Confirm the four labels above exist. They already exist in owainlewis/machinist.
3. Merge the workflow through the normal pull-request process. The issue event
   uses the workflow on the default branch.
4. As the repository owner, open an issue with a clear title and description.
   For example: “Demo: settings page crashes when saving an empty name”.
   Explain the expected behavior and the observed crash in the body.
5. Open Actions → Luna issue triage. After both jobs succeed, the issue should
   have the `bug` label. The demo creates no comments, commits, or pull requests.

The first job passes bounded issue text as JSON data to Codex in a read-only
permission profile with the Action's `drop-sudo` protection. It has no GitHub
write permissions. A separate job validates the structured output and adds only
an allowed, existing label to the issue identified by the GitHub event. It
preserves other labels and skips closed issues or issues already carrying one
of the four categories. It reads the issue back to confirm the write.

The workflow fails clearly if the API secret is missing, classification fails,
the model returns invalid output, or GitHub rejects the label. Rerun the failed
workflow after fixing the cause. A rerun still classifies the original event
text; it does not respond to subsequent edits.

## Verification

Run `node --test .github/scripts/issue-triage.test.cjs` for input handling,
output validation, label application, duplicate handling, and failure cases.
The tests run in CI and `just check`. A live model call requires the API secret
and a new owner-authored issue after merge; unit tests do not prove model access
or classification quality.
