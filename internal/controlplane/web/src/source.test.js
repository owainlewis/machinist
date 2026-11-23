import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

const now = new Date().toISOString();
const fromLinear = { id: "t_lin", title: "Fix the toasts", repo: "owner/repo", agent: "default", state: "pr_ready", source: "linear", source_url: "https://linear.app/team/issue/ENG-158/fix", pr_number: 12, pr_state: "open", created_at: now, updated_at: now };
const fromCurl = { id: "t_api", title: "Bump the deps", repo: "owner/repo", agent: "default", state: "done", source: "api", pr_number: 9, pr_state: "merged", created_at: now, updated_at: now };

async function load(context, file) {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  return server.ssrLoadModule(file);
}

test("the jobs list shows each job's source and its pull request as a chip", { timeout: 30000 }, async context => {
  const { JobsPage } = await load(context, "/src/jobs-page.jsx");
  const saved = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true, value: { getItem: () => "list", setItem() {} } });
  context.after(() => saved ? Object.defineProperty(globalThis, "localStorage", saved) : delete globalThis.localStorage);
  const list = renderToStaticMarkup(React.createElement(JobsPage, { tickets: [fromLinear, fromCurl], runners: [], loaded: true }));

  const rows = [...list.matchAll(/<tr>(.*?)<\/tr>/g)].map(match => match[1]).filter(row => row.includes("<td"));
  const linear = rows.find(row => row.includes("Fix the toasts"));
  const curl = rows.find(row => row.includes("Bump the deps"));
  assert.match(linear, /<span class="source" data-source="linear"><svg[^>]*>.*?<\/svg><span>Linear<\/span><\/span>/);
  assert.match(curl, /data-source="api"><svg[^>]*>.*?<\/svg><span>API<\/span>/);
  assert.match(linear, /<a class="pr-chip" data-state="open" href="https:\/\/github.com\/owner\/repo\/pull\/12" target="_blank" rel="noreferrer" aria-label="Pull request #12, open">.*<span>#12<\/span><span class="pr-chip-state">Open<\/span><\/a>/);
  assert.match(curl, /class="pr-chip" data-state="merged"[^>]*>.*Merged<\/span><\/a>/);

  for (const label of ["Running", "Done", "Failed", "Needs a person", "Cancelled"]) assert.ok(list.includes(`>${label}<span class="filter-chip-count">`), label);
  assert.match(list, /<option value="" selected="">All sources<\/option><option value="api">API<\/option><option value="linear">Linear<\/option>/);
});

test("a job from Linear links to its issue, and one from curl links nowhere", { timeout: 30000 }, async context => {
  const { JobView } = await load(context, "/src/job-page.jsx");
  const page = ticket => renderToStaticMarkup(React.createElement(JobView, { detail: { ticket, runs: [] } }));

  const linear = page(fromLinear);
  assert.match(linear, /<dt>Source<\/dt><dd class="job-source"><span class="source" data-source="linear">.*?Linear<\/span><\/span><a href="https:\/\/linear.app\/team\/issue\/ENG-158\/fix" target="_blank" rel="noreferrer">Open in Linear/);

  const curl = page(fromCurl);
  assert.match(curl, /<dt>Source<\/dt><dd class="job-source"><span class="source" data-source="api">.*?API<\/span><\/span><\/dd>/);

  const unsafe = page({ ...fromLinear, source_url: "javascript:alert(1)" });
  assert.doesNotMatch(unsafe, /javascript:/);
});

test("filtering the jobs list by source or state shows only those jobs", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "localStorage", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  dom.window.localStorage.setItem("machinist-jobs-view", "list");
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
  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  const fromWeb = { ...fromCurl, id: "t_web", title: "Tidy the README", source: "web", state: "failed", pr_number: 0 };
  await act(async () => root.render(React.createElement(JobsPage, { tickets: [fromLinear, fromCurl, fromWeb], runners: [], loaded: true })));
  const titles = () => [...document.querySelectorAll(".jobs tbody .job-title")].map(cell => cell.textContent).sort();
  assert.deepEqual(titles(), ["Bump the deps", "Fix the toasts", "Tidy the README"]);

  const select = document.querySelector('select[aria-label="Source"]');
  await act(async () => {
    select.value = "linear";
    select.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  });
  assert.deepEqual(titles(), ["Fix the toasts"]);

  await act(async () => {
    select.value = "";
    select.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  });
  const chip = document.querySelector('.filter-chip[data-filter="failed"]');
  await act(async () => chip.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.equal(chip.getAttribute("aria-pressed"), "true");
  assert.deepEqual(titles(), ["Tidy the README"]);

  // Clicking the chosen chip again shows every job.
  await act(async () => chip.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.equal(titles().length, 3);
});

test("the dashboard breaks jobs down by source", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/dashboard" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  const jobs = (total, done, failed) => ({ total, active: 0, done, failed, cancelled: 0 });
  const stats = {
    days: 14, jobs: jobs(5, 4, 1), runs: { total: 5, succeeded: 4, failed: 1, cancelled: 0 }, success_rate: 0.8,
    run_millis: { median: 60000, p90: 90000 }, wait_millis: { median: 1000, p90: 2000 }, tokens: 10, cost: 0, daily: [],
    presets: [], repos: [],
    sources: [{ name: "linear", jobs: jobs(3, 3, 0), run_millis: 60000 }, { name: "api", jobs: jobs(2, 1, 1), run_millis: null }],
  };
  globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => stats });
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
  const { DashboardPage } = await server.ssrLoadModule("/src/dashboard-page.jsx");
  await act(async () => root.render(React.createElement(DashboardPage, { onSignedOut() {} })));
  for (let i = 0; i < 50 && !document.querySelector('[aria-labelledby="sources-table"] tbody'); i++) {
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
  const rows = [...document.querySelectorAll('[aria-labelledby="sources-table"] tbody tr')];
  assert.deepEqual(rows.map(row => [row.querySelector("th").textContent, row.querySelector("td").textContent]), [["Linear", "3"], ["API", "2"]]);
  assert.ok(rows[0].querySelector('th .source[data-source="linear"] svg'), "each source has its icon");
});
