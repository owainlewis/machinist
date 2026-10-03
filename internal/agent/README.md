# Claude runtime

Machinist uses the maintained `@agentclientprotocol/claude-agent-acp` adapter pinned
in `runtime/package-lock.json`. Install it once with:

```sh
npm ci --prefix internal/agent/runtime --ignore-scripts
```

Claude must already be signed in on the execution host. The adapter uses those
credentials. Machinist never installs dependencies or bypasses permissions during
a turn. An explicitly configured ACP executable can replace the bundled path.

Each turn starts an ACP process, initializes it, creates or loads the saved
provider session, and streams structured messages. Historical messages replayed
by load are suppressed because Machinist already stores its transcript. Unknown
client requests fail explicitly. Permission requests default to denial and only
allow an individual action when the application callback returns true. Plan mode
is an additional provider restriction, not an OS sandbox.

Run protocol tests with `go test -race ./internal/agent`. The opt-in live test
uses host Claude credentials in a disposable temporary directory and checks
streaming, session reload, denied file creation, and cancellation:

```sh
MACHINIST_LIVE_CLAUDE=1 go test -v ./internal/agent -run TestLiveClaude -count=1
```
