# Credentials

Credentials live on runners. The control plane never holds an agent credential.

A credential is the list of environment variable names the harness already reads, passed
through unchanged. So keys, service accounts, federation and cloud providers need no special
handling. On a Kubernetes runner the same variables live in one Secret. A runner offers only
the harnesses it has a credential for.

Run `machinist runner check --repo owner/name` to see which credential source a harness
actually used.

## Which credential to use

| Credential | Team runner | Own machine, own work | Notes |
| --- | --- | --- | --- |
| Anthropic API key, `ANTHROPIC_API_KEY` | Yes | Yes | Billed to the key owner |
| Bedrock, Google Cloud or Foundry variables | Yes | Yes | Billed through the provider |
| OpenAI API key, `OPENAI_API_KEY` | Yes | Yes | The runner runs `codex login --with-api-key` for each run |
| Codex access token, `CODEX_ACCESS_TOKEN` | Business or Enterprise only | Business or Enterprise only | Acts as the person who created it. Not yet tested here |
| Claude login, `login = true` | No | Yes | Local runners only |
| Claude `setup-token`, `CLAUDE_CODE_OAUTH_TOKEN` | No | Yes | Local or Kubernetes |
| ChatGPT login, `login = true` | No | Yes | Local runners only, with `slots = 1` |

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
they show what each harness sends, not a completed model turn.

| Test | Result |
| --- | --- |
| `codex app-server` with `OPENAI_API_KEY`, private home | No credentials sent, `account` is null |
| `codex app-server` with `CODEX_API_KEY`, private home | No credentials sent, `account` is null |
| `codex app-server` with a fake `CODEX_ACCESS_TOKEN` | No credentials sent. Inconclusive, because Codex needs a valid token format |
| `codex login --with-api-key` into a private home, then `codex app-server` | `Authorization: Bearer` with the key, account type `apiKey` |
| Claude spec command with `ANTHROPIC_API_KEY`, private home | Sent as `x-api-key` |
| Claude spec command with `CLAUDE_CODE_OAUTH_TOKEN`, private home | Sent as `Authorization: Bearer` |
| Codex, clone not listed as trusted, full access | Sends the "until the project is trusted" `configWarning`, then trusts the folder and loads project config |
| Codex, trust written to private `config.toml` first | No warning, project config loaded |
| Codex, trust passed with `-c` | Warning still sent |
| `claude auth status` with a private `HOME` on a logged-in Mac | Not logged in |
| `codex login status` with a private `CODEX_HOME` on a logged-in Mac | Not logged in. The directory must exist first |

### Continuing a stopped run

One real Claude run on a local fixture repository with a stub `gh`. The agent pushed all
its work in one push, and it was then stopped before it reported. A second run in a fresh
clone received only the ticket log. It read the review and the CI failure through `gh`,
fixed both, kept the earlier commits, passed the tests and returned a valid report.

**Not tested:** continuing a branch where the requested work is unfinished, a real GitHub
pull request, and any of this with Codex.
