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
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [{ name: "mac-mini", last_contact: new Date().toISOString() }, { name: "lost", last_contact: "2026-01-01T00:00:00Z" }] }));
  for (const want of [/mac-mini/, /Online/, /Not responding/, /Last contact/, /1 of 2 online, 0 of 1 slot busy/]) assert.match(html, want);
  assert.match(renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [] })), /machinist runner start/);
});

test("Runners shows where each runner runs, its slots and what it is doing", async context => {
  const { RunnersPage, runnerKindLabel } = await load(context);
  assert.equal(runnerKindLabel("local"), "Local");
  assert.equal(runnerKindLabel("kubernetes"), "Kubernetes");
  assert.equal(runnerKindLabel("docker"), "Docker");
  assert.equal(runnerKindLabel("docker (runsc)"), "Docker (runsc)");
  const runners = [
    { name: "cluster", kind: "kubernetes", slots: 4, last_contact: ago(5), jobs: [
      { run_id: "r1", ticket_id: "t_1", title: "Add dark mode", kind: "agent", started_at: ago(120) },
      { run_id: "r2", ticket_id: "t_2", title: "Fix login", kind: "agent", started_at: ago(30) },
    ] },
    { name: "mac-mini", kind: "local", slots: 1, last_contact: ago(5), jobs: [] },
    { name: "docker-box", kind: "docker", slots: 2, last_contact: ago(5), jobs: [] },
    { name: "gvisor-box", kind: "docker (runsc)", slots: 2, last_contact: ago(5), jobs: [] },
  ];
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners, queue: { waiting: 3, oldest_queued_at: ago(250) } }));
  for (const want of [/Kubernetes/, /Local/, /Docker<\/span>/, /Docker \(runsc\)/, /2 of 4 slots busy/, /0 of 1 slot busy/, /4 of 4 online, 2 of 9 slots busy/, /href="#\/jobs\/t_1"/, /Add dark mode/, /Fix login/, /3 runs are waiting for a runner, the oldest for 4m 1\ds/]) assert.match(html, want);
  assert.doesNotMatch(html, /cannot start/);
});

test("the queue line says how many runs wait and for how long", async context => {
  const { queueLine, stalled } = await load(context);
  const now = Date.parse("2026-11-10T09:01:00Z");
  assert.equal(queueLine(undefined, now), "");
  assert.equal(queueLine({ waiting: 0 }, now), "");
  assert.equal(queueLine({ waiting: 1, oldest_queued_at: "2026-11-10T09:00:20Z" }, now), "1 run has waited 40s for a runner");
  assert.equal(queueLine({ waiting: 2, oldest_queued_at: "2026-11-10T09:02:00Z" }, now), "2 runs are waiting for a runner, the oldest for 0s");
  assert.equal(queueLine({ waiting: 1 }, now), "1 run is waiting for a runner");
  assert.equal(stalled([{ last_contact: "2026-11-10T08:00:00Z" }], { waiting: 1 }, now), true);
  assert.equal(stalled([{ last_contact: "2026-11-10T09:00:30Z" }], { waiting: 1 }, now), false);
  assert.equal(stalled([], { waiting: 0 }, now), false);
});

test("the slot meter lights one segment per busy slot", async context => {
  const { SlotMeter } = await load(context);
  const meter = renderToStaticMarkup(React.createElement(SlotMeter, { busy: 2, slots: 4 }));
  assert.match(meter, /aria-label="2 of 4 slots busy"/);
  assert.equal(meter.match(/<span data-busy="true">/g).length, 2);
  assert.equal(meter.match(/<span/g).length, 5);
  const large = renderToStaticMarkup(React.createElement(SlotMeter, { busy: 8, slots: 32 }));
  assert.match(large, /slot-meter-bar/, "a large runner gets one bar, not 32 segments");
  assert.match(large, /width:25%/);
});

test("Runners shows the agent harnesses each runner has", async context => {
  const { RunnersPage, runnerHarnesses } = await load(context);
  assert.deepEqual(runnerHarnesses({ harnesses: ["steps", "agy", "claude"] }), ["agy", "claude"]);
  assert.deepEqual(runnerHarnesses({ harnesses: ["steps"] }), []);
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [{ name: "vm", harnesses: ["steps", "agy", "claude"], last_contact: new Date().toISOString() }, { name: "old", harnesses: ["steps"], last_contact: new Date().toISOString() }] }));
  assert.match(html, /<span>Antigravity<\/span>.*<span>Claude Code<\/span>/);
  assert.equal((html.match(/runner-harnesses/g) || []).length, 1);
});

test("Runners says which harness queued runs wait for, and offers to remove idle offline runners", async context => {
  const { RunnersPage, harnessWaits, removable } = await load(context);
  assert.deepEqual(harnessWaits(undefined), []);
  assert.deepEqual(harnessWaits({ waiting: 3, blocked: [{ harnesses: ["codex"], waiting: 2 }, { harnesses: ["agy", "claude"], waiting: 1 }] }), [
    "2 runs are waiting for a runner with Codex",
    "1 run is waiting for a runner with Antigravity and Claude Code",
  ]);
  const now = Date.parse("2026-11-10T09:01:00Z");
  assert.equal(removable({ last_contact: "2026-11-01T09:00:00Z", jobs: [] }, now), true);
  assert.equal(removable({ last_contact: "2026-11-01T09:00:00Z", jobs: [{ run_id: "r1" }] }, now), false, "a runner holding a job stays");
  assert.equal(removable({ last_contact: "2026-11-10T09:00:30Z", jobs: [] }, now), false, "an online runner stays");

  const runners = [
    { name: "laptop", harnesses: ["steps", "claude"], probed: true, last_contact: ago(5), jobs: [] },
    { name: "old-laptop", last_contact: "2026-01-01T00:00:00Z", jobs: [] },
  ];
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners, queue: { waiting: 1, blocked: [{ harnesses: ["codex"], waiting: 1 }] } }));
  assert.match(html, /1 run is waiting for a runner with Codex\. No online runner has it\./);
  assert.match(html, /Install the harness on a runner/);
  assert.equal((html.match(/aria-label="Remove /g) || []).length, 1);
  assert.match(html, /aria-label="Remove old-laptop"/);
  const two = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners, queue: { waiting: 1, blocked: [{ harnesses: ["agy", "codex"], waiting: 1 }] } }));
  assert.match(two, /1 run is waiting for a runner with Antigravity and Codex\. No online runner has them\./);
  const shared = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners, queue: { waiting: 3, blocked: [{ harnesses: ["codex"], waiting: 2 }, { harnesses: ["codex"], waiting: 1 }] } }));
  assert.match(shared, /No online runner has it\./);
  const clear = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners, queue: { waiting: 1 } }));
  assert.doesNotMatch(clear, /waiting for a runner with/);
});
