# Route Codex coding tasks through Machinist

Machinist ships a user-level Codex skill in [`skills/machinist`](../skills/machinist/).
It changes the default owner of state-changing coding requests: the interactive Codex chat
creates or reuses a GitHub issue and dispatches it to the Machinist foreman instead of
editing the repository itself.

Read-only discussion, design, investigation, and review remain in the current chat. An
explicit request to implement in the current chat also overrides the default.

## Install the skill

Build and initialize Machinist first:

```sh
mkdir -p ./bin
go build -o ./bin/machinist ./cmd/machinist
./bin/machinist init
```

Then link the tracked skill into the user-level Codex skill directory:

```sh
mkdir -p ~/.codex/skills
ln -s "$(pwd)/skills/machinist" ~/.codex/skills/machinist
```

Run that command from the Machinist repository root and use an absolute checkout path.
Start a new Codex task after installation so Codex refreshes its skill inventory.

The helper looks for `machinist` on `PATH`, then for `bin/machinist` in this source
checkout. Set `MACHINIST_BIN` to an absolute executable path to override both locations.

## What happens on a coding request

The dispatching Codex chat:

1. resolves the current repository and its GitHub remote;
2. reuses a supplied open issue or creates one from the request;
3. starts `machinist run --agent=foreman` with that issue; and
4. streams the supervised run until the foreman returns a verified pull request or an
   explicit human or infrastructure blocker.

The chat does not fall back to editing code if dispatch fails. Fix the reported Machinist,
GitHub CLI, Codex CLI, or configuration problem and retry.

Machinist sets `MACHINIST_RUN_ID` in every executor process. The skill treats that marker
as a recursion guard, so a foreman or worker that can see the same global skill continues
its assigned role instead of starting another Machinist run.

## Verify

Run the focused skill check with:

```sh
./skills/machinist/test.sh
```

The test proves argument preservation, Git root resolution, URL validation, and recursive
delegation refusal without starting a real coding agent or writing to GitHub.
