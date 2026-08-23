# Phases

A phase is one versioned prompt file. A dispatch names the phase and the work.

```sh
factory run 412 --repo acme/api --phase build
```

Phases live in `.factory/phases/<name>.md` inside the repository the work
changes, so a prompt is reviewed, versioned, and rolled back like the code it
operates on.

## A phase file

```markdown
---
name: verify
description: Check a change against its acceptance criteria and report only
runtime: claude
timeout: 30m
---
You are reviewing work you did not do. Assume it is wrong until the code shows
otherwise.

Acceptance criteria:
{{ run.criteria }}

Report only. Do not fix anything.
```

Frontmatter is a leading `---` block of flat `key: value` pairs. Nested
structures are rejected rather than ignored.

| Key | Required | Meaning |
| --- | --- | --- |
| `description` | yes | What the phase does |
| `name` | no | Must match the file name when set |
| `runtime` | no | `claude`, `codex`, or `pi`. Defaults to the control-plane default |
| `model` | no | Recorded for future runtime selection |
| `timeout` | no | A duration such as `30m` or `2h`. Defaults to 2h |
| `permissions` | no | `read-only` or `write`. Defaults to `write` |

Any other key is kept and is readable in the body as `{{ phase.<key> }}`, which
is how a phase declares a bound such as `max_findings` without Factory
modelling it.

## Phases never chain themselves

A phase file describes one prompt and says nothing about what runs next. The
sequence is chosen at dispatch:

```sh
factory run 412 --repo acme/api --phase plan
factory run 412 --repo acme/api --phase build
factory run 412 --repo acme/api --phase verify
```

This is the line that keeps Factory from growing a pipeline router. Where
conditional routing is needed, it belongs to the operator deciding to dispatch
again, not to a field in a markdown file.

`--phases plan,build,verify` is reserved for the same sequence as one atomic
dispatch. It is refused today because holding a later phase until an earlier one
succeeds needs a dependency edge the control plane does not have yet.

## Template variables

Rendering is strict. An unknown variable fails the render rather than
substituting an empty string, because a prompt that silently lost its acceptance
criteria is worse than a prompt that did not run.

There are exactly three namespaces.

| Namespace | Available | Keys |
| --- | --- | --- |
| `run` | always | `repo`, `phase`, `title`, `body`, `criteria` |
| `issue` | only when the work is an issue | `identifier`, `number`, `title`, `body`, `url`, `state`, `labels` |
| `phase` | always | every frontmatter key of the phase |

A phase that targets a repository rather than an issue, such as `audit`, must
not reference `issue.*`.

## Naming the work

Three forms are accepted:

```sh
factory run 412 --repo acme/api                       # like gh
factory run https://github.com/acme/api/issues/412    # what is on the clipboard
factory run acme/api#412                              # how GitHub renders it
factory run --repo acme/api --phase audit             # no issue at all
```

Issue text is read through `gh`, which is already a requirement and is already
authenticated on the operator's machine.

## Checking a phase before it runs

```sh
factory phases                                     # list phases and versions
factory validate                                   # fail on any broken file
factory run 412 --repo acme/api --phase build --dry-run
```

`--dry-run` renders the prompt and dispatches nothing, which is the loop for
editing a phase file. Run `factory validate` in CI so a broken phase cannot
merge.

## Versioning

Every phase carries the SHA-256 of its file. A dispatch records that hash with
the rendered prompt, so "which prompt produced this pull request" stays
answerable after the file has changed.

```sh
$ factory phases
PHASE   VERSION  RUNTIME  TIMEOUT  DESCRIPTION
audit   46a8567  claude   1h0m0s   Find real defects in a repository and file…
build   2a2d6d1  claude   2h0m0s   Implement one scoped issue and open a pull request
plan    f71d182  claude   30m0s    Turn a scoped issue into an implementation plan…
verify  f1fd590  claude   30m0s    Check a change against its acceptance criteria…
```

## No task should need the operator

The phases in this repository tell an agent to take the most reasonable reading
of an ambiguous issue, record the assumption in the pull request, and continue.
A run that stops to ask a question has failed to be useful, and the assumption
is visible on a pull request the operator was going to read anyway.

## Current limits

- One phase per dispatch. Chaining needs a dependency edge in the control plane.
- No `--worker` targeting. Work routes by repository access, runtime, and
  capacity.
- Each dispatch records one task, and the control plane caps tasks, so a long
  history eventually needs the schema collapse tracked in the CLI issue.
- `factory web` and `factory worker` are not subcommands yet. Start the control
  plane and worker with `just run`.
