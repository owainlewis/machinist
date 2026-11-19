import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("sign in, find a job, then read what its steps printed", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "getComputedStyle", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  let signedIn = false;
  const now = new Date().toISOString();
  const ticket = { id: "t_new", state: "done", repo: "owner/repo", agent: "coding", pr_number: 44, title: "Center the toasts", created_by: "alice", created_at: now, updated_at: now };
  const older = { ...ticket, id: "t_old", title: "An older job", pr_number: undefined, created_at: "2026-11-01T00:00:00Z" };
  const run = { id: "run_1", ticket_id: "t_new", kind: "agent", runner: "runner-1", status: "failed", started_at: now, ended_at: now, stages: [{ name: "checkout", state: "succeeded", duration_millis: 5088 }, { name: "test", state: "failed", duration_millis: 900, error: "command exited with status 1" }, { name: "publish", state: "skipped" }] };
  const events = [
    { type: "step.started", message: "name=checkout" }, { type: "process.output", encoding: "base64", data: btoa("Cloning into 'repo'\n") },
    { type: "step.started", message: "name=test" }, { type: "process.output", encoding: "base64", data: btoa("FAIL tests/styles.test.ts\n") },
  ].map((event, index) => ({ sequence: index + 1, time: now, ...event }));
  let releaseOld, releaseRun, holdRun = false;
  let reruns = 0;
  const started = [];
  const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/v1/session" && options.method === "POST") {
      const { token } = JSON.parse(options.body);
      if (token !== "good") return reply(401, { error: "invalid token" });
      signedIn = true;
      return reply(200, { name: "alice" });
    }
    if (!signedIn) return reply(401, { error: "a token is required" });
    if (url === "/api/v1/repositories") return reply(200, { repositories: ["owner/repo"], name: "alice" });
    if (url === "/api/v1/tickets") return reply(200, { tickets: [older, ticket] });
    if (url === "/api/v1/tickets/t_new/rerun" && options.method === "POST") { reruns++; return reply(200, { ...ticket, state: "working" }); }
    if (url.startsWith("/api/v1/stats?days=")) {
      const days = Number(url.split("=")[1]);
      const daily = Array.from({ length: days }, (_, index) => ({ day: `2026-11-${String(index + 1).padStart(2, "0")}`, jobs: 1, succeeded: index % 2, failed: 1, cancelled: 0 }));
      const group = { name: "coding", jobs: { total: 3, active: 1, done: 1, failed: 1, cancelled: 0 }, run_millis: 90000 };
      return reply(200, { days, jobs: { total: 3, active: 1, done: 1, failed: 1, cancelled: 0 }, runs: { total: 4, succeeded: 3, failed: 1, cancelled: 0 }, success_rate: 0.75, run_millis: { median: 90000, p90: 300000 }, wait_millis: { median: 2000, p90: 9000 }, tokens: 12345, cost: 1.5, daily, presets: [group], repos: [{ ...group, name: "owner/repo" }] });
    }
    if (url === "/api/v1/presets") return reply(200, { default: "coding", presets: [{ name: "coding", steps: ["checkout", "implement", "publish"] }, { name: "spec", steps: ["checkout", "write"] }] });
    if (url === "/machinist/run" && options.method === "POST") {
      started.push(JSON.parse(options.body));
      if (holdRun) return new Promise(resolve => { releaseRun = () => resolve(reply(201, { id: "t_new", state: "queued" })); });
      return reply(201, { id: "t_new", state: "queued" });
    }
    if (url === "/api/v1/runners") return reply(200, { runners: [], queue: { waiting: 0 } });
    if (url === "/api/v1/tickets/t_old") return new Promise(resolve => { releaseOld = () => resolve(reply(200, { ticket: older, entries: [], runs: [] })); });
    if (url === "/api/v1/tickets/t_new") return reply(200, { ticket, entries: [{ id: 1, kind: "request", author: "alice", body: "Center the toasts\n\nThey sit off to the right.", created_at: now }], runs: [run], steps: ["checkout", "test", "publish"] });
    if (url === "/api/v1/runs/run_1/events?after=0") return reply(200, { cursor: events.length, events });
    throw new Error(`unexpected ${url}`);
  };
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  let root;
  context.after(async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    await act(async () => root?.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  globalThis.IS_REACT_ACT_ENVIRONMENT = false;
  root = (await server.ssrLoadModule("/src/main.jsx")).appRoot;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;

  await eventually(() => assert.ok(document.querySelector("#token")));
  await type("#token", "bad");
  await act(async () => document.querySelector("form button").click());
  await eventually(() => assert.match(document.querySelector('[role="alert"]').textContent, /not valid/));
  await type("#token", "good");
  await act(async () => document.querySelector("form button").click());

  // Jobs open as a board, and the List button shows the same jobs as rows.
  await eventually(() => assert.ok(document.querySelector(".board-card")));
  assert.equal(document.querySelector("h1").textContent, "Jobs");
  assert.deepEqual([...document.querySelectorAll("nav a")].map(link => link.textContent), ["Dashboard", "Jobs", "Runners"]);
  assert.deepEqual([...document.querySelectorAll(".board-column-head")].map(head => head.textContent), ["Queued0", "Running0", "Done2"]);
  await act(async () => [...document.querySelectorAll(".range button")].find(button => button.textContent === "List").click());
  await eventually(() => assert.ok(document.querySelector("table.jobs tbody tr")));
  assert.equal(localStorage.getItem("machinist-jobs-view"), "list");
  assert.deepEqual([...document.querySelectorAll(".job-title")].map(cell => cell.textContent), ["Center the toasts", "An older job"]);
  assert.equal(document.querySelector('nav a[aria-current="page"]').textContent, "Jobs");

  await act(async () => document.querySelector('a[href="#/jobs/t_new"]').click());
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Center the toasts"));
  assert.match(document.querySelector(".job-head .pill").textContent, /Done/);
  assert.ok(document.querySelector('a[href="https://github.com/owner/repo/pull/44"]'));
  await eventually(() => assert.match(document.querySelector(".step-output")?.textContent || "", /FAIL tests\/styles.test.ts/));
  assert.deepEqual([...document.querySelectorAll(".step-name")].map(name => name.textContent), ["checkout", "test", "publish"]);
  assert.equal(document.querySelectorAll(".step-output").length, 1, "only the failed step opens by itself");
  await act(async () => document.querySelector('.step[data-state="succeeded"] .step-row').click());
  await eventually(() => assert.ok([...document.querySelectorAll(".step-output")].some(pane => /Cloning into/.test(pane.textContent))));
  assert.equal(document.querySelector('.step[data-state="skipped"] .step-row').disabled, true);
  assert.deepEqual([...document.querySelectorAll(".pipeline-node")].map(node => node.getAttribute("aria-label")), ["checkout: Passed", "test: Failed", "publish: Skipped"]);
  await act(async () => document.querySelector('.step[data-state="succeeded"] .step-row').click());
  await act(async () => document.querySelector('.pipeline li[data-state="succeeded"] .pipeline-node').click());
  await eventually(() => assert.ok([...document.querySelectorAll(".step-output")].some(pane => /Cloning into/.test(pane.textContent))));
  await act(async () => [...document.querySelectorAll("button")].find(button => button.textContent === "Run again").click());
  await eventually(() => assert.equal(reruns, 1));

  await act(async () => { window.location.hash = "#/dashboard"; });
  await eventually(() => assert.equal(document.querySelector(".stat-tile:nth-child(3) .stat-value")?.textContent, "75%"));
  assert.equal(document.querySelector("h1").textContent, "Dashboard");
  assert.equal(document.querySelectorAll('[aria-labelledby="runs-chart"] .chart-column').length, 14);
  assert.match(document.querySelector("#presets-table").parentElement.textContent, /coding.*50%.*1m 30s/);
  await act(async () => document.querySelector('.range button:nth-child(1)').click());
  await eventually(() => assert.equal(document.querySelectorAll('[aria-labelledby="runs-chart"] .chart-column').length, 7));
  await act(async () => document.querySelector(".chart-column").dispatchEvent(new window.MouseEvent("mouseover", { bubbles: true })));
  await eventually(() => assert.match(document.querySelector(".chart-tooltip")?.textContent || "", /Nov 1.*Succeeded 0.*Failed 1/));

  // Start a job from the form, and land on its page.
  await act(async () => { window.location.hash = "#/jobs"; });
  await eventually(() => assert.ok(document.querySelector('a[href="#/new"]')));
  await act(async () => document.querySelector('.page-head a[href="#/new"]').click());
  await eventually(() => assert.ok(document.querySelector("#new-job-preset")));
  assert.equal(document.querySelector("h1").textContent, "New job");
  assert.equal(document.querySelector('nav a[aria-current="page"]').textContent, "Jobs");
  assert.equal(document.querySelector("#new-job-preset").value, "coding", "the default preset is chosen");
  assert.equal(document.querySelector("#new-job-repo").value, "owner/repo");
  assert.equal(document.querySelector("#new-job-preset").title, "checkout, implement, publish");
  const start = () => [...document.querySelectorAll("form button")].find(button => /Start job/.test(button.textContent));
  assert.equal(start().disabled, true, "a job needs a prompt");
  await choose("#new-job-preset", "spec");
  await type("#new-job-prompt", "  Write a spec for dark mode\n\nCover the settings page.  ");
  assert.match(document.querySelector(".field-hint").textContent, /Write a spec for dark mode/);
  await act(async () => start().click());
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Center the toasts"));
  assert.equal(started.length, 1);
  assert.match(started[0].key, /^ui-/);
  assert.deepEqual({ ...started[0], key: undefined }, { key: undefined, preset: "spec", vars: { repo: "owner/repo" }, prompt: "Write a spec for dark mode\n\nCover the settings page." });

  // A job that starts after someone has left the form must not pull them back.
  holdRun = true;
  await act(async () => { window.location.hash = "#/new"; });
  await eventually(() => assert.ok(document.querySelector("#new-job-prompt")));
  await type("#new-job-prompt", "Fix the footer");
  await act(async () => start().click());
  await eventually(() => assert.ok(releaseRun));
  await act(async () => { window.location.hash = "#/runners"; });
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Runners"));
  await act(async () => { releaseRun(); await new Promise(resolve => setTimeout(resolve, 50)); });
  assert.equal(window.location.hash, "#/runners");
  holdRun = false;

  // A reply for a job someone has left must not show under the next one.
  await act(async () => { window.location.hash = "#/jobs/t_old"; });
  await eventually(() => assert.ok(releaseOld));
  await act(async () => { window.location.hash = "#/jobs/t_new"; });
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Center the toasts"));
  await act(async () => { releaseOld(); await new Promise(resolve => setTimeout(resolve, 50)); });
  assert.equal(document.querySelector("h1").textContent, "Center the toasts");

  signedIn = false;
  await eventually(() => assert.ok(document.querySelector("#token")), 300);
});

async function type(selector, value) {
  const element = document.querySelector(selector);
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "value").set;
  await act(async () => {
    setter.call(element, value);
    element.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}

async function choose(selector, value) {
  const element = document.querySelector(selector);
  await act(async () => {
    element.value = value;
    element.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
}

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}
