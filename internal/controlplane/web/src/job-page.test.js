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
    ticket: { ...ticket, state: "pr_ready", pr_number: 12, pr_state: "open" },
    runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now, report: { summary: "Left open: auto-merge is off." } }],
  });
  assert.match(needsYou, /class="pill pill-warning"[^>]*>.*<span>Needs you<\/span><\/span>/);
  assert.match(needsYou, />Run again<\/button>/);
  assert.doesNotMatch(needsYou, />Cancel<\/button>/);

  const done = page({ ticket: { ...ticket, state: "done" }, runs: [{ id: "run_1", status: "succeeded", started_at: now, ended_at: now }] });
  assert.match(done, /class="pill pill-success"[^>]*>.*<span>Done<\/span><\/span>/);
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
