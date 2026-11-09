# Security

## Reporting

Do not open a public issue for an unpatched vulnerability. Email
[owain@owainlewis.com](mailto:owain@owainlewis.com) with the affected revision,
impact, reproduction steps, and any suggested mitigation. Do not include real
credentials or private repository data.

You should receive an acknowledgement within seven days. The maintainer will
coordinate remediation and disclosure after the report is understood.

## Supported versions

Machinist is under active development. Only the latest release and the latest
commit on `main` receive security fixes. Go 1.26.6 is the minimum supported
toolchain. Dependency and toolchain minimums may increase when a security fix
requires it.

## Trust model

**An agent can do whatever the GitHub login allows.** Issue text, PR comments and repository
files can be written by outsiders, and that text can steer an agent. A mention requires
write access, but that limits who starts work, not what an issue says. The real limits are
the login's permissions and branch protection. On a repository that accepts outside pull
requests, automatic merging needs branch protection with human review.

**Agents can read their credentials.** An agent can read its model credential and GitHub
token. Use dedicated service accounts with narrow scopes and provider spend limits. Every
`GH_TOKEN` should be fine-grained, limited to connected repositories, without admin rights,
and belong to the bot.

**One token reaches many repositories.** A prompt injection in one repository can reach any
other repository the token covers. Run one deployment per trust domain.

**Machinist tokens are powerful.** Any token holder can read run history, claim runs, and
assign work that tells an agent to reveal its credentials. Treat a token as equal to the
GitHub login and the model credential. Give each person and runner their own, so it can be
revoked with `machinist token revoke`. Tokens are stored as hashes.

**Keep the control plane private.** Every route needs a token, except `/healthz`, sign-in
and static assets. Tokens are sent in the clear unless the network or TLS protects them, and
nothing stops an operator exposing the address publicly. Keep it on a private network, or
set `tls_cert` and `tls_key`.

**Local runners.** Concurrent runs under one OS user can read each other's files and
environments, and the agent has open network access. Use a dedicated OS account. If a runner
shares an OS user with the control plane, an agent can read and change its database.

**Kubernetes runners.** Each run is a gVisor Job with no service account token. An init
container with no credentials proves the network is locked down before the Secret enters
the Pod. The egress proxy blocks all but listed hosts. It does not stop data leaving through
an allowed service, such as a push to another repository on `github.com`.

## Local data

Machinist state defaults to `~/.machinist`. Protect this directory. It holds the ticket
database, run history and the runner token.
