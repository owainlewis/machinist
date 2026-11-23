# Job sources and how a tracker plugs in

*   **Status:** Accepted
*   **Date:** 2026-11-23
*   **Issue:** ENG-158

## Every job has a source

A job starts in one of a few places. Machinist records which one as the job's **source**,
with a link to the original issue or message when there is one.

| Source | Where the job started | Link |
| --- | --- | --- |
| `web` | The New job form in the web app | None |
| `api` | `POST /machinist/run` or `POST /api/v1/tickets` called with a token, such as from `curl` | The GitHub issue, when the request named one |
| `schedule` | An automation, on its schedule or run by hand | None |
| `linear` | An issue given to the Machinist app in Linear | The Linear issue |
| `jira`, `github`, `slack`, … | Trackers still to come, one ticket each | The issue or message |

The source is stored on the ticket (`tickets.origin`) with the link (`tickets.source_url`).
The run API returns them as `source` and `source_url`, `GET /machinist/jobs?source=linear`
filters by them, and `GET /api/v1/stats` counts jobs by them in `sources`. The web app shows
the source as an icon on every job, filters the list by it, and links the job page to the
original.

The web app and `curl` send the same request. The control plane tells them apart by how
the request signed in: the web app's session cookie means `web`, and an `Authorization:
Bearer` header means `api`.

A link is kept only if it is an absolute `http` or `https` URL, so a link the web app shows
can never run script.

### Jobs from before sources

When the store opens, every ticket without a source gets one from what it already has:

| The ticket has | Source | Link |
| --- | --- | --- |
| Intake `schedule:<name>` | `schedule` | None |
| Intake `run`, and a key starting `ui-` (the New job form's keys) | `web` | None |
| Intake `run` or `api` otherwise | `api` | The GitHub issue, if it has an issue number |
| Any other intake, such as `linear` | That name | The URL on the last line of its prompt, `Issue ENG-1: https://…` |

The ticket's `source` column keeps its old meaning, the **intake** (`run`, `api`, `linear`
or `schedule:<name>`). Automations, idempotency keys and the tracker poller still use it.

## A tracker is a `sources.Tracker`

A tracker is a place people hand work to Machinist and hear back from it. Machinist polls
it; nothing in the tracker calls Machinist, so it needs no public route. A tracker
implements one interface, in `internal/sources/tracker.go`:

```go
type Tracker interface {
	Name() string
	Assignments(ctx context.Context, since time.Time) ([]Assignment, error)
	Start(ctx context.Context, issue string) error
	Replies(ctx context.Context, since, until time.Time) ([]Reply, error)
	Return(ctx context.Context, issue, comment string) error
	Comment(ctx context.Context, issue, body string) error
	Fail(ctx context.Context, issue, reason string) error
	Finish(ctx context.Context, issue string) error
	States(ctx context.Context, issues []string) (map[string]IssueState, error)
}
```

`sources.Poller` does everything else, the same way for every tracker:

- It keeps a cursor per tracker, so a restart neither misses nor repeats an assignment.
- It applies `TrackerRules`: who may send work, which preset runs, and which repository
  each project or team maps to.
- It caps jobs per hour.
- It starts each job through the one intake path, with the tracker's name as the job's
  intake and source and `Assignment.URL` as its link.
- It writes each job's news back once (In Progress, the pull request, Done on merge, the
  result or the failure) and records what it wrote in `tracker_reports`.
- It runs a job again when an allowed person replies on its issue, and cancels a job whose
  issue leaves Machinist.

Because the source is the tracker's `Name()`, a new tracker shows up in the jobs list, its
filter and the dashboard with no change outside its own package. Only its icon and label
need a line each in the web app (`source-mark.jsx` and `sourceLabels` in `jobs.js`); until
then it shows a generic icon and its name.

## Worked example: Linear

`sources.Linear` in `internal/sources/linear.go` is the only tracker today.

1. **Name.** `Name()` returns `"linear"`. That is the job's source, the prefix of its key
   (`linear:<issue id>`), the poller's cursor name, and the author of what Machinist writes.
2. **Assignments.** It asks Linear's GraphQL API for issues delegated or assigned to the
   Machinist app and updated after `since`, oldest first. Each becomes an `Assignment` with
   a stable `Key`, the issue's `Title`, `Body`, `Identifier`, `URL`, `Team` and `Project`,
   and the `Actor` who handed it over. `URL` is the link the job page shows.
3. **Writing back.** `Start`, `Return`, `Fail` and `Finish` move the issue between Linear
   workflow states and comment on it. `Comment` posts a comment.
4. **Replies.** `Replies` returns comments people left on Machinist's issues in a window
   of time, and fails with `ErrTooManyReplies` rather than return part of them, so the
   poller can ask for a shorter window.
5. **States.** `States` says whether each issue is still assigned to Machinist and still in
   a started state, so the poller can cancel a job whose issue moved.
6. **Wiring.** `linearPoller` in `internal/controlplane/server.go` reads the `linear`
   section of `config.yaml`, checks that every mapped repository is connected, and builds a
   `sources.Poller` around the tracker. The server runs it beside the engine.

## Adding the next tracker

Jira, GitHub Issues and Slack each get their own ticket. Each one:

1. Adds a type in `internal/sources` that implements `Tracker`, with `Name()` returning
   `jira`, `github` or `slack`, and `Assignment.URL` set to the issue or message.
2. Adds a config section and a constructor like `linearPoller` that builds its `Poller`.
3. Adds its icon and label in the web app, if `jobs.js` does not already have them.
4. Tests it against a fake of the tracker's API, as `linear_test.go` does for Linear.

Two parts of the poller still speak Linear and should become tracker-neutral with the
second tracker: the comment for an issue that maps to no repository names
`linear.projects` and `linear.teams`, and `TrackerRules` maps Linear's projects and teams.
A Slack source has messages, not issues, so it may also need `Start`, `Return` and `Finish`
to be no-ops, or a smaller interface that the poller checks for.
