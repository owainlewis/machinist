import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

async function load(context) {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  return server.ssrLoadModule("/src/runners.jsx");
}

const ago = seconds => new Date(Date.now() - seconds * 1000).toISOString();

test("Runners shows each runner and when it last made contact", async context => {
  const { RunnersPage, runnerOnline } = await load(context);
  const now = Date.parse("2026-10-15T20:00:00Z");
  assert.equal(runnerOnline({ last_contact: "2026-10-15T19:59:00Z" }, now), true);
  assert.equal(runnerOnline({ last_contact: "2026-10-15T19:58:00Z" }, now), false);
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [{ name: "mac-mini", harnesses: ["claude", "codex"], last_contact: new Date().toISOString() }, { name: "lost", harnesses: ["claude"], last_contact: "2026-01-01T00:00:00Z" }] }));
  for (const want of [/mac-mini/, /claude, codex/, /Online/, /Not responding/, /Last contact/, /1 of 2 online, 0 of 1 slot busy/]) assert.match(html, want);
  assert.match(renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [] })), /machinist runner start/);
});

test("Runners shows where each runner runs, its slots and what it is doing", async context => {
  const { RunnersPage } = await load(context);
  const runners = [
    { name: "cluster", kind: "kubernetes", harnesses: ["claude"], slots: 4, last_contact: ago(5), jobs: [
      { run_id: "r1", ticket_id: "t_1", title: "Add dark mode", kind: "agent", started_at: ago(120) },
      { run_id: "r2", ticket_id: "t_2", title: "Fix login", kind: "reviewer", reviewer: "security", started_at: ago(30) },
    ] },
    { name: "mac-mini", kind: "local", harnesses: ["codex"], slots: 1, last_contact: ago(5), jobs: [] },
  ];
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners, queue: { waiting: 3, oldest_queued_at: ago(250) } }));
  for (const want of [/Kubernetes/, /Local/, /2 of 4 slots busy/, /0 of 1 slot busy/, /2 of 2 online, 2 of 5 slots busy/, /href="#\/work\/t_1"/, /Add dark mode/, /Reviewer: security/, /3 runs are waiting for a runner, the oldest for 4m 1\ds/]) assert.match(html, want);
  assert.doesNotMatch(html, /cannot start/);
});

test("the queue line says how many runs wait and for how long", async context => {
  const { queueLine, stalled, runningJobs } = await load(context);
  const now = Date.parse("2026-11-10T09:01:00Z");
  assert.equal(queueLine(undefined, now), "");
  assert.equal(queueLine({ waiting: 0 }, now), "");
  assert.equal(queueLine({ waiting: 1, oldest_queued_at: "2026-11-10T09:00:20Z" }, now), "1 run has waited 40s for a runner");
  assert.equal(queueLine({ waiting: 2, oldest_queued_at: "2026-11-10T09:02:00Z" }, now), "2 runs are waiting for a runner, the oldest for 0s");
  assert.equal(queueLine({ waiting: 1 }, now), "1 run is waiting for a runner");
  assert.equal(stalled([{ last_contact: "2026-11-10T08:00:00Z" }], { waiting: 1 }, now), true);
  assert.equal(stalled([{ last_contact: "2026-11-10T09:00:30Z" }], { waiting: 1 }, now), false);
  assert.equal(stalled([], { waiting: 0 }, now), false);
  const jobs = runningJobs([{ name: "a", jobs: [{ run_id: "new", started_at: "2026-11-10T09:00:30Z" }] }, { name: "b", jobs: [{ run_id: "old", started_at: "2026-11-10T08:00:00Z" }] }, { name: "c" }]);
  assert.deepEqual(jobs.map(job => [job.run_id, job.runner]), [["old", "b"], ["new", "a"]]);
});
