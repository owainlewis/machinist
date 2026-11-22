import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

// The board and the job page are rendered as HTML, as the browser first gets them.
test("a running job shows its step, how long it has run and what it is doing", { timeout: 30000 }, async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");

  const minutesAgo = minutes => new Date(Date.now() - minutes * 60000).toISOString();
  const ticket = { id: "t_1", title: "Center the toasts", repo: "owner/repo", agent: "default", state: "working", created_at: minutesAgo(9), updated_at: minutesAgo(1) };
  const job = { run_id: "run_1", ticket_id: "t_1", started_at: minutesAgo(9), step: "work", step_started_at: minutesAgo(7), action: "Run go test ./..." };
  const board = renderToStaticMarkup(React.createElement(JobsPage, { tickets: [ticket], runners: [{ name: "runner-1", jobs: [job] }], loaded: true }));
  assert.match(board, /<span class="board-card-step">work · 7m \ds<\/span>/);
  assert.ok(board.includes('<span class="board-card-action" title="Run go test ./...">Run go test ./...</span>'), board);

  assert.match(board, /<li class="board-card" data-state="running">/, "the running card is marked so it can show it is live");
  assert.match(board, /<span class="board-dot" data-column="running" aria-hidden="true"><\/span>Running/);

  const quiet = renderToStaticMarkup(React.createElement(JobsPage, { tickets: [ticket], runners: [{ name: "runner-1", jobs: [{ ...job, step: "", action: "" }] }], loaded: true }));
  assert.doesNotMatch(quiet, /board-card-now/, "a job with no progress yet shows none");
});

test("a run that saved its change as a patch links to it from the job page", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  const now = new Date().toISOString();
  const ticket = { id: "t_1", title: "Use Postgres", repo: "owner/repo", agent: "default", state: "failed", created_at: now, updated_at: now };
  const run = { id: "run_1", ticket_id: "t_1", status: "failed", started_at: now, ended_at: now, stages: [{ name: "publish", state: "failed" }] };
  const page = patch => renderToStaticMarkup(React.createElement(JobView, { detail: { ticket, runs: [{ ...run, patch }] } }));
  assert.match(page(true), /<a href="\/api\/v1\/runs\/run_1\/patch" download="">.*Download the change as a patch<\/a>/);
  assert.match(page(true), /Apply it with <code>git am<\/code>/);
  assert.doesNotMatch(page(undefined), /run-patch/, "a run with no patch links to none");
});

test("a queued job names the harness no online runner has", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  const now = new Date().toISOString();
  const ticket = { id: "t_1", title: "Review it", repo: "owner/repo", agent: "review", state: "working", created_at: now, updated_at: now };
  const run = { id: "run_1", ticket_id: "t_1", status: "queued" };
  const page = waiting_for => renderToStaticMarkup(React.createElement(JobView, { detail: { ticket, runs: [run], waiting_for } }));
  assert.match(page(["codex"]), /Waiting for a runner with Codex/);
  assert.match(page(["codex"]), /No online runner has Codex, which this job&#x27;s agents run, so it cannot start/);
  assert.match(page(undefined), /Waiting for a runner</);
  assert.doesNotMatch(page(undefined), /page-warning/);
});
