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

Machinist runs configured coding commands with the operating-system permissions,
tools, environment, and credentials of the worker host user. Command prompts,
executor commands, repository mappings, and worker configuration are trusted
operator policy. A submitted work prompt is untrusted input, but the selected
command can still act on it using every capability available to its process.

The control plane binds only to loopback. Browser requests use a random CSRF
token, while CLI and worker requests use a shared bearer token. A managed worker
may connect to a non-loopback control plane only over HTTPS. Do not expose the
control plane through an untrusted proxy or network boundary.

Repository mappings constrain Machinist assignment and path resolution. They do
not sandbox a command from other files or tools available to the worker OS user.
Use OS permissions, repository permissions, and narrowly scoped credentials to
enforce capability boundaries.

## Local data

Machinist state defaults to `~/.machinist`. Protect this directory because it
may contain task prompts, command output, control-plane state, bearer tokens,
repository paths, and unpublished work. Configuration and token files should
remain readable only by the worker host user.

Provider CLIs own their credentials. Machinist does not request or persist
provider API tokens.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the implemented security boundaries.
