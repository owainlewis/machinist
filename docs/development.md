# Development

## Requirements

- Go 1.26.6 or newer
- Node.js 22.22.2 and npm for the control-plane frontend
- `just` to use the repository shortcuts

## Build

Build the React application, embed it, and compile Machinist:

```sh
just build
```

The binary is written to `bin/machinist`.

For backend-only changes that do not touch the frontend, the tracked production
assets allow a direct Go build:

```sh
go build ./...
```

## Run locally

```sh
./bin/machinist init
```

Set `github.handle` and `github.repositories` in `~/.machinist/config.toml`. Use a repository
you control and a token for a bot account. Never point experiments at live state.

```sh
just local
```

## Verify

Run the complete project check before opening a pull request:

```sh
just check
```

This installs the locked frontend dependencies, runs frontend tests, rebuilds the embedded
assets, checks and vets the Go code, runs Go tests with the race detector, and builds all
Go packages.

Focused commands are also available:

```sh
just test
python3 -m unittest discover -s deploy/kubernetes -p 'test_*.py'
(cd internal/controlplane/web && npm test)
go test ./internal/runner
```

CI requires `check`, `Linux checks`, and `macOS checks` on the current PR
revision. Run frontend builds before Go checks, not concurrently: Go embeds
`web/dist`, so replacing that directory during a build can race file discovery.
For infrastructure changes, distinguish fake-client tests from live cluster proof.
Never put runtime archives or authentication files in build contexts.

## Tests

New tests use a fake harness and a fake `gh` from `internal/fakegh`. `internal/e2e` runs the
real control plane and a real local runner. `internal/agentrunner/conformance_test.go` runs one
suite on both runner types. On Kubernetes only `kubectl` is fake. It reads the real Job manifest
and starts the real sandbox code.

The live tests spend real money and write to a real repository, so they are opt-in. They need
a disposable repository, a bot and a person with write access, and a key for each harness.
`internal/e2e/live_test.go` lists every variable.

```sh
MACHINIST_NATIVE_LIVE=1 go test ./internal/runner ./internal/e2e -run Live -v -count=1 -timeout 60m
```

## Project layout

See [Architecture](../ARCHITECTURE.md#packages).

The frontend source lives in `internal/controlplane/web/src`. Its production
bundle lives in `internal/controlplane/web/dist` because Go embeds those files at
compile time.
