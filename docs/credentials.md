# Credentials

Credentials live on runners. The control plane never holds a credential a step uses.

A credential is a list of environment variable names that a tool already reads, passed
through unchanged with `env` in `runner.yaml`. So keys, service accounts, federation and
cloud providers need no special handling. On a Kubernetes runner the same variables live in
one Secret. See [Runners](runners.md#what-steps-can-use).

```yaml
env: [ANTHROPIC_API_KEY, LINEAR_API_KEY]
```

Every step of every preset on that runner gets these variables. Run presets that need
different credentials on different runners.

## GitHub token scopes

With a [GitHub App](github-app.md), each run gets its own token for its repository, which
lasts an hour, and the rest of this section does not apply.

Without one, steps get the runner's `GH_TOKEN`, so `gh` and `git` can clone, push and open
pull requests.
`machinist runner check` and `machinist runner start` print one warning if a classic token
reports scopes beyond `repo` (private repositories), `public_repo` (public repositories)
and `workflow` (changes to `.github/workflows/`). The warning names the extra scopes, such
as `delete_repo` or `admin:public_key`: any step can use these permissions too.

They also print one warning if a classic token lacks `workflow`, because GitHub refuses a
push that changes `.github/workflows/` without it. Add the scope with
`gh auth refresh -s workflow`.

Use a token limited to the repositories the bot works on. Fine-grained tokens do
not report OAuth scopes, so they produce no scope warning. A missing warning is
not a check of their repository permissions. The token value is never printed,
and the warning does not stop the runner. If GitHub cannot report scopes, the
runner continues without a scope warning. Kubernetes runners use the token in
their job Secret, so this local token check does not apply to them.

## Which credential to use

| Credential | Team runner | Own machine, own work | Notes |
| --- | --- | --- | --- |
| Anthropic API key, `ANTHROPIC_API_KEY` | Yes | Yes | Billed to the key owner |
| Bedrock, Google Cloud or Foundry variables | Yes | Yes | Billed through the provider |
| OpenAI API key, `OPENAI_API_KEY` | Yes | Yes | A Codex step runs `codex login --with-api-key` itself |
| Claude login, `login: true` | No | Yes | Local runners only |
| Claude `setup-token`, `CLAUDE_CODE_OAUTH_TOKEN` | No | Yes | Local or Kubernetes |
| ChatGPT login, `login: true` | No | Yes | Local runners only, with `slots: 1` |

This table is a reading of the vendor pages below, not legal advice. Check your own
agreement with each provider.

*   Anthropic says OAuth is for ordinary use by subscription purchasers. Its advertised
    plan limits assume individual use. API keys are for products and for customers'
    authorized users. See [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance).
*   OpenAI calls API keys the right way to authenticate automation. A ChatGPT login in CI
    needs a trusted private runner, one machine or one job stream at a time, and no public
    repositories. See [CI/CD authentication](https://learn.chatgpt.com/docs/auth/ci-cd-auth)
    and [access tokens](https://learn.chatgpt.com/docs/enterprise/access-tokens).

## Spike results

Run on 2026-11-09 on macOS with Claude Code 2.1.319 and Codex 0.157.1. Credential tests
used fake values against a local server that recorded the headers and returned 401, so
they show what each tool sends, not a completed model turn.

| Test | Result |
| --- | --- |
| `codex app-server` with `OPENAI_API_KEY`, private home | No credentials sent, `account` is null |
| `codex login --with-api-key` into a private home, then `codex app-server` | `Authorization: Bearer` with the key, account type `apiKey` |
| Claude with `ANTHROPIC_API_KEY`, private home | Sent as `x-api-key` |
| Claude with `CLAUDE_CODE_OAUTH_TOKEN`, private home | Sent as `Authorization: Bearer` |
| `claude auth status` with a private `HOME` on a logged-in Mac | Not logged in |
| `codex login status` with a private `CODEX_HOME` on a logged-in Mac | Not logged in. The directory must exist first |
