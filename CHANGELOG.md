# Changelog

User-visible changes, compatibility notes, and required operator actions are
recorded here. This project follows [Semantic Versioning](https://semver.org/).

## Unreleased

### Added

- One `factory` command for starting the existing server or Worker and reading
  current Runs, Run details, and Workers through the loopback API. The
  `factory-server` and `factory-worker` commands remain available for
  compatibility.
- Reproducible Linux and macOS release archives for amd64 and arm64.
- Embedded version and source commit reporting in all Factory binaries.
- SPDX SBOMs, SHA-256 checksums, and generated third-party license notices.

### Changed

- The operator model is now Task, Run, and Session. Routine, Work, and Target
  are gone from the UI, the HTTP API JSON, the protocol types, the database
  schema, and the documentation. Execution and Attempt remain internal records;
  a Session shows its attempt number and retry history.

### Compatibility

- The Task, Run, and Session rename is compatibility-breaking.
  `/api/v1/routines` becomes `/api/v1/tasks` and `/api/v1/work` becomes
  `/api/v1/runs`, with `/work/{id}/targets/{id}` becoming
  `/runs/{id}/sessions/{id}`. Migration 30 renames the database tables in place
  without data loss and refuses to run if a `tasks`, `task_repositories`,
  `runs`, or `sessions` table already exists. Worker registration now sends
  `claim_protocol_version`, and the claim protocol moves to version 2 because
  the claim payload replaced its `target` field with `session`, so a Worker
  registered before the upgrade must re-register before it can claim. The agent
  environment exposes `FACTORY_RUN_ID` and `FACTORY_SESSION_ID`. Upgrade the
  server and every Worker together.
- Factory has not published a stable release yet. The first tagged release will
  define the initial supported upgrade boundary.

[Unreleased]: https://github.com/owainlewis/factory/compare/HEAD
