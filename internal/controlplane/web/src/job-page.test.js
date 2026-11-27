import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const now = new Date().toISOString();
const ticket = { id: "t_1", title: "Fix the toasts", repo: "owner/repo", agent: "default", created_at: now, updated_at: now };

test("a failed job says why at the top, and a result's links can be clicked", { timeout: 30000 }, async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  const page = detail => renderToStaticMarkup(React.createElement(JobView, { detail }));

  const failed = page({
    ticket: { ...ticket, state: "failed" },
    entries: [{ id: 1, kind: "request", author: "alice", body: "Fix it", created_at: now }, { id: 2, kind: "message", author: "machinist", body: "Machinist could not get a GitHub token from the GitHub App: not installed.", created_at: now }],
    runs: [{ id: "run_1", status: "failed", started_at: now, ended_at: now }],
  });
  const panel = /<section class="panel job-failure"[^>]*>(.*?)<\/section>/.exec(failed);
  assert.ok(panel, failed);
  assert.match(panel[1], /could not get a GitHub token from the GitHub App: not installed\.</);
  assert.ok(failed.indexOf("job-failure") < failed.indexOf("Attempt 1"), "the reason comes before the attempts");
  assert.doesNotMatch(failed, /class="pipeline"/, "a run with no steps draws no empty pipeline");

  const reported = page({
    ticket: { ...ticket, state: "failed" },
    entries: [{ id: 1, kind: "message", author: "machinist", body: "The tests would not pass.", created_at: now }],
    runs: [
      { id: "run_1", status: "succeeded", started_at: now, ended_at: now, report: { summary: "An older result." } },
      { id: "run_2", status: "failed", started_at: now, ended_at: now, report: { summary: "The tests would not pass." } },
    ],
  });
  assert.match(reported, /class="panel job-failure"/, reported);
  assert.equal(reported.split("The tests would not pass.").length - 1, 2, "the reason shows once in the panel and once in History");
  assert.doesNotMatch(reported, /id="job-result"/, "a failed job shows no Result beside its reason");
  assert.doesNotMatch(reported, /An older result\./, "an earlier attempt's result does not show under the reason");

  const done = page({
    ticket: { ...ticket, state: "done" },
    runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now, report: { summary: "Merged: https://github.com/owner/repo/pull/642" } }],
  });
  assert.match(done, /Merged: <a href="https:\/\/github.com\/owner\/repo\/pull\/642" target="_blank" rel="noreferrer">https:\/\/github.com\/owner\/repo\/pull\/642<\/a>/);
  assert.doesNotMatch(done, /job-failure/, "a job that passed shows no failure");
});

test("a run that failed before any step started says no steps ran", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs/t_1" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  const asked = [];
  globalThis.fetch = async url => {
    asked.push(url);
    return { ok: true, status: 200, json: async () => ({ events: [], cursor: 0 }) };
  };
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  const detail = { ticket: { ...ticket, state: "failed" }, runs: [{ id: "run_1", status: "failed", started_at: now, ended_at: now }] };
  await act(async () => root.render(React.createElement(JobView, { detail })));
  for (let i = 0; i < 50 && !document.body.textContent.includes("No steps ran."); i++) {
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
  assert.deepEqual(asked, ["/api/v1/runs/run_1/events?after=0"]);
  assert.match(document.querySelector(".run").textContent, /No steps ran\./);
  assert.doesNotMatch(document.body.textContent, /Loading steps/);
});

test("pr_ready stands out as Needs you, and active jobs offer Cancel instead of Run again", { timeout: 30000 }, async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  const page = detail => renderToStaticMarkup(React.createElement(JobView, { detail }));

  const queued = page({ ticket: { ...ticket, state: "working" }, runs: [{ id: "run_1", status: "queued" }] });
  assert.match(queued, />Cancel<\/button>/);
  assert.doesNotMatch(queued, />Run again<\/button>/);

  const running = page({ ticket: { ...ticket, state: "working" }, runs: [{ id: "run_1", status: "running", started_at: now }] });
  assert.match(running, />Cancel<\/button>/);
  assert.doesNotMatch(running, />Run again<\/button>/);

  const needsYou = page({
    ticket: { ...ticket, state: "pr_ready", pr_number: 12, pr_state: "open", pr_state_at: now },
    steps: [{ name: "merge-gate", agents: [] }, { name: "merge", agents: [] }],
    runs: [{
      id: "run_1",
      status: "succeeded",
      started_at: now,
      ended_at: now,
      report: { summary: "Left open: merge gate requires human review." },
      stages: [
        { name: "merge-gate", state: "succeeded", duration_millis: 12000 },
        { name: "merge", state: "succeeded", duration_millis: 1000 },
      ],
    }],
  });
  assert.match(needsYou, /class="pill pill-warning"[^>]*>.*<span>Needs you<\/span><\/span>/);
  assert.match(needsYou, /data-state="held"[^>]*>.*merge.*Held/);
  assert.match(needsYou, /class="job-pr-value">.*#12<\/a> · Open <time/);
  assert.match(needsYou, />Run again<\/button>/);
  assert.doesNotMatch(needsYou, />Cancel<\/button>/);

  const heldShipping = page({
    ticket: { ...ticket, state: "pr_ready", pr_number: 13, pr_state: "open", pr_state_at: now, shipping: '{"hold":"merge gate requires human review"}' },
    steps: [{ name: "merge-gate", agents: [] }, { name: "merge", agents: [] }],
    runs: [{
      id: "run_1",
      status: "succeeded",
      started_at: now,
      ended_at: now,
      stages: [
        { name: "merge-gate", state: "succeeded", duration_millis: 12000 },
        { name: "merge", state: "succeeded", duration_millis: 1000 },
      ],
    }],
  });
  assert.match(heldShipping, /class="pill pill-warning"[^>]*>.*<span>Needs you<\/span><\/span>/);
  assert.match(heldShipping, /data-state="held"[^>]*>.*merge.*Held/);
  assert.match(heldShipping, />Run again<\/button>/);
  assert.doesNotMatch(heldShipping, />Cancel<\/button>/);

  const waitingForCI = page({
    ticket: { ...ticket, state: "pr_ready", pr_number: 14, pr_state: "open", pr_state_at: now, shipping: '{"since":"2026-11-27T15:00:00Z","head":"abc"}' },
    steps: [{ name: "merge-gate", agents: [] }, { name: "merge", agents: [] }],
    runs: [{
      id: "run_1",
      status: "succeeded",
      started_at: now,
      ended_at: now,
      stages: [
        { name: "merge-gate", state: "succeeded", duration_millis: 12000 },
        { name: "merge", state: "succeeded", duration_millis: 1000 },
      ],
    }],
  });
  assert.match(waitingForCI, /class="pill pill-info"[^>]*>.*state-working.*<span>Waiting for CI<\/span><\/span>/);
  assert.doesNotMatch(waitingForCI, /data-state="held"/, "auto-merging PR waiting on CI must not mark merge Held");
  assert.doesNotMatch(waitingForCI, /Needs you/);
  assert.match(waitingForCI, />Cancel<\/button>/);
  assert.doesNotMatch(waitingForCI, />Run again<\/button>/);

  const done = page({
    ticket: { ...ticket, state: "done" },
    steps: [{ name: "merge", agents: [] }],
    runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now, stages: [{ name: "merge", state: "succeeded", duration_millis: 1000 }] }],
  });
  assert.match(done, /class="pill pill-success"[^>]*>.*<span>Done<\/span><\/span>/);
  assert.doesNotMatch(done, /data-state="held"/);
  assert.match(done, />Run again<\/button>/);
  assert.doesNotMatch(done, />Cancel<\/button>/);
});

test("clicking Cancel on an active job posts to cancel and refreshes, or shows an error", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs/t_1" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "MouseEvent", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  const calls = [];
  let failCancel = false;
  globalThis.fetch = async (url, init = {}) => {
    calls.push({ url, method: init.method || "GET" });
    if (url.endsWith("/cancel")) {
      if (failCancel) return { ok: false, status: 409, json: async () => ({ error: "ticket has already ended" }) };
      return { ok: true, status: 200, json: async () => ({ ...ticket, state: "cancelled" }) };
    }
    return { ok: true, status: 200, json: async () => ({ events: [], cursor: 0 }) };
  };
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  let refreshed = 0;
  const detail = { ticket: { ...ticket, state: "working" }, runs: [{ id: "run_1", status: "running", started_at: now }] };
  await act(async () => root.render(React.createElement(JobView, { detail, onChange: () => { refreshed++; } })));

  const cancelButton = () => [...document.querySelectorAll(".job-actions button")].find(button => button.textContent.includes("Cancel"));
  assert.ok(cancelButton(), "expected a Cancel button");
  await act(async () => cancelButton().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.ok(calls.some(call => call.url === "/api/v1/tickets/t_1/cancel" && call.method === "POST"), `calls = ${JSON.stringify(calls)}`);
  assert.equal(refreshed, 1);

  failCancel = true;
  await act(async () => cancelButton().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.match(document.querySelector('.job-actions [role="alert"]').textContent, /ticket has already ended/);
});

test("JobPage stops polling on terminal jobs and restarts polling on Run again", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs/t_1" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "MouseEvent", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  const scheduled = new Map();
  let nextTimerID = 1;
  dom.window.setTimeout = (fn, delay) => {
    const id = nextTimerID++;
    scheduled.set(id, { fn, delay });
    return id;
  };
  dom.window.clearTimeout = id => { scheduled.delete(id); };

  const ticketRequests = [];
  const responses = [
    { ticket: { ...ticket, state: "working" }, entries: [], runs: [{ id: "run_1", status: "running", started_at: now }] },
    { ticket: { ...ticket, state: "done", pr_number: 12, pr_state: "merged" }, entries: [], runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now }] },
    { ticket: { ...ticket, state: "working" }, entries: [], runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now }, { id: "run_2", status: "queued" }] },
  ];
  let responseIndex = 0;
  let rerunCalls = 0;
  globalThis.fetch = async (url, init = {}) => {
    if (url === "/api/v1/tickets/t_1" && (!init.method || init.method === "GET")) {
      const payload = responses[Math.min(responseIndex++, responses.length - 1)];
      ticketRequests.push(payload.ticket.state);
      return { ok: true, status: 200, json: async () => payload };
    }
    if (url === "/api/v1/tickets/t_1/rerun" && init.method === "POST") {
      rerunCalls++;
      return { ok: true, status: 200, json: async () => ({ ...ticket, state: "working" }) };
    }
    return { ok: true, status: 200, json: async () => ({ events: [], cursor: 0 }) };
  };
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  const { JobPage } = await server.ssrLoadModule("/src/job-page.jsx");
  await act(async () => root.render(React.createElement(JobPage, { jobID: "t_1" })));

  assert.deepEqual(ticketRequests, ["working"]);
  const pollTimers = () => [...scheduled.entries()].filter(([, entry]) => entry.delay === 2000);
  assert.ok(pollTimers().length >= 1, "active job should schedule a 2s poll timer");

  // Fire scheduled 2s timers; ticket transitions to terminal done+merged state.
  for (const [id, entry] of pollTimers()) {
    scheduled.delete(id);
    await act(async () => { await entry.fn(); });
  }
  assert.deepEqual(ticketRequests, ["working", "done"]);
  assert.equal(pollTimers().length, 0, "terminal job must stop scheduling 2s poll timers");

  // Clicking Run again restarts polling.
  const rerunButton = [...document.querySelectorAll(".job-actions button")].find(button => button.textContent.includes("Run again"));
  assert.ok(rerunButton, "expected Run again button on terminal job");
  await act(async () => rerunButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.equal(rerunCalls, 1);
  assert.deepEqual(ticketRequests, ["working", "done", "working"]);
  assert.ok(pollTimers().length >= 1, "clicking Run again must restart 2s polling");
});

test("pollJob stops only on terminal states with no active run and no open or shipping PR", { timeout: 30000 }, async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { pollJob } = await server.ssrLoadModule("/src/job-page.jsx");

  assert.equal(pollJob(null), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "working" }, runs: [] }), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "pr_ready", pr_number: 1, pr_state: "open" }, runs: [] }), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "pr_ready", pr_number: 1, pr_state: "open", shipping: '{"since":"2026-11-27T15:00:00Z","head":"abc"}' }, runs: [] }), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "done" }, runs: [{ id: "run_1", status: "succeeded", ended_at: now }] }), false);
  assert.equal(pollJob({ ticket: { ...ticket, state: "done", pr_number: 1, pr_state: "merged" }, runs: [{ id: "run_1", status: "succeeded", ended_at: now }] }), false);
  assert.equal(pollJob({ ticket: { ...ticket, state: "failed" }, runs: [{ id: "run_1", status: "failed", ended_at: now }] }), false);
  assert.equal(pollJob({ ticket: { ...ticket, state: "cancelled" }, runs: [{ id: "run_1", status: "cancelled", ended_at: now }] }), false);
  assert.equal(pollJob({ ticket: { ...ticket, state: "cancelled" }, runs: [{ id: "run_1", status: "cancelled" }] }), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "cancelled" }, runs: [{ id: "run_1", status: "running" }] }), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "failed", pr_number: 1, pr_state: "open" }, runs: [{ id: "run_1", status: "failed", ended_at: now }] }), true);
  assert.equal(pollJob({ ticket: { ...ticket, state: "done", shipping: '{"hold":""}' }, runs: [{ id: "run_1", status: "succeeded", ended_at: now }] }), true);
});

test("JobView shows the Merge gate panel for low, medium, and high verdicts and omits it when absent", { timeout: 30000 }, async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  const { JobView } = await server.ssrLoadModule("/src/job-page.jsx");
  const page = overrides => renderToStaticMarkup(React.createElement(JobView, {
    detail: {
      ticket: { ...ticket, state: "pr_ready", pr_number: 42, pr_state: "open", ...overrides },
      runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now }],
    },
  }));

  const approvedLow = page({
    state: "done",
    pr_state: "merged",
    merge_gate_decision: "approve",
    merge_gate_risk: "low",
    merge_gate_reason: "Low-risk UI copy fix covered by unit tests.",
  });
  assert.match(approvedLow, /<section class="panel merge-gate" data-decision="approve" data-risk="low" aria-labelledby="job-merge-gate">/);
  assert.match(approvedLow, /<h2 id="job-merge-gate" class="label">Merge gate<\/h2>/);
  assert.match(approvedLow, /class="merge-gate-decision merge-gate-decision-approved"[^>]*>.*<span>Approved<\/span><\/span>/);
  assert.match(approvedLow, /class="risk-badge" data-risk="low"[^>]*>.*<span>Low<\/span><\/span>/);
  assert.match(approvedLow, /Low-risk UI copy fix covered by unit tests\./);
  assert.ok(approvedLow.indexOf("job-merge-gate") < approvedLow.indexOf("Attempt 1"), "merge gate panel sits before attempts");

  const mediumRisk = page({
    merge_gate_decision: "approve",
    merge_gate_risk: "medium",
    merge_gate_reason: "Changes API response schema.",
  });
  assert.match(mediumRisk, /<section class="panel merge-gate" data-decision="approve" data-risk="medium"/);
  assert.match(mediumRisk, /<span>Approved<\/span>/);
  assert.match(mediumRisk, /class="risk-badge" data-risk="medium"[^>]*>.*<span>Medium<\/span><\/span>/);
  assert.match(mediumRisk, /Changes API response schema\./);

  const highRisk = page({
    merge_gate_decision: "human_review",
    merge_gate_risk: "high",
    merge_gate_reason: "Alters authentication token handling and session cookies.",
  });
  assert.match(highRisk, /<section class="panel merge-gate" data-decision="human_review" data-risk="high"/);
  assert.match(highRisk, /class="merge-gate-decision merge-gate-decision-review"[^>]*>.*<span>Human review<\/span><\/span>/);
  assert.match(highRisk, /class="risk-badge" data-risk="high"[^>]*>.*<span>High<\/span><\/span>/);
  assert.match(highRisk, /Alters authentication token handling and session cookies\./);

  const reasonOnly = page({ merge_gate_reason: "Held for manual verification." });
  assert.match(reasonOnly, /<section class="panel merge-gate"/);
  assert.match(reasonOnly, /Held for manual verification\./);
  assert.doesNotMatch(reasonOnly, /class="risk-badge"/);
  assert.doesNotMatch(reasonOnly, /class="merge-gate-decision/);

  const absent = page({});
  assert.doesNotMatch(absent, /job-merge-gate/, "panel is omitted when no merge-gate fields exist");
  assert.doesNotMatch(absent, /class="risk-badge"/);
});
