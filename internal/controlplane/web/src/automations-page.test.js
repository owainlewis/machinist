import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

function setupDOM(initialView = "board") {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "MouseEvent", "KeyboardEvent", "Event", "localStorage", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  dom.window.localStorage.setItem("machinist-jobs-view", initialView);
  return {
    dom,
    cleanup() {
      dom.window.close();
      for (const [name, descriptor] of previous) {
        if (descriptor) Object.defineProperty(globalThis, name, descriptor);
        else delete globalThis[name];
      }
    },
  };
}

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}

async function typeInto(element, value) {
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "value").set;
  await act(async () => {
    setter.call(element, value);
    element.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}

test("Automations view lists automations, supports empty state, and runs as-is or with an edited prompt", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM("board");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });

  const now = Date.now();
  const nextDue1 = new Date(now + 2 * 60 * 60 * 1000).toISOString();
  const nextDue2 = new Date(now + 26 * 60 * 60 * 1000).toISOString();
  const updatedAt = new Date(now - 5 * 60 * 1000).toISOString();

  let automations = [
    {
      name: "nightly-review",
      repo: "owner/app",
      agent: "default",
      schedule: "0 9 * * 1-5",
      timezone: "UTC",
      next_due: nextDue1,
      prompt: "Review open pull requests and summarize blockers.",
      last_ticket: {
        id: "t_last1",
        state: "done",
        repo: "owner/app",
        agent: "default",
        title: "nightly-review",
        created_at: updatedAt,
        updated_at: updatedAt,
        outcome: "Reviewed 2 PRs.",
      },
    },
    {
      name: "weekly-deps",
      repo: "owner/api",
      agent: "maintainer",
      schedule: "0 6 * * 1",
      timezone: "America/New_York",
      next_due: nextDue2,
      prompt: "Audit outdated dependencies.",
    },
  ];

  const runCalls = [];
  let failNextRun = false;

  globalThis.fetch = async (url, options = {}) => {
    const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
    if (url.startsWith("/api/v1/tickets")) {
      return reply(200, { tickets: [], total: 0, limit: 20, offset: 0, has_more: false, done_total: 0, done_limit: 20 });
    }
    if (url === "/api/v1/automations") {
      return reply(200, { automations });
    }
    if (url.startsWith("/api/v1/automations/") && url.endsWith("/run") && options.method === "POST") {
      runCalls.push({ url, body: options.body ? JSON.parse(options.body) : undefined });
      if (failNextRun) {
        failNextRun = false;
        return reply(400, { error: "prompt cannot be empty" });
      }
      const id = `t_run_${runCalls.length}`;
      return reply(201, { id, state: "working", repo: "owner/app", agent: "default", title: "nightly-review" });
    }
    throw new Error(`unexpected ${url}`);
  };

  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  await act(async () => root.render(React.createElement(JobsPage, { runners: [], onSignedOut() {} })));

  // View toggle shows Board, List, and Automations.
  await eventually(() => assert.ok(document.querySelector(".range")));
  assert.deepEqual([...document.querySelectorAll(".range button")].map(btn => btn.textContent), ["Board", "List", "Automations"]);

  // Switch to Automations view.
  const automationsBtn = [...document.querySelectorAll(".range button")].find(btn => btn.textContent === "Automations");
  await act(async () => automationsBtn.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));

  await eventually(() => assert.equal(document.querySelectorAll("table.automations tbody tr").length, 2));
  assert.equal(dom.window.localStorage.getItem("machinist-jobs-view"), "automations");
  assert.equal(document.querySelector(".job-filters"), null, "job filters stay hidden in Automations view");

  const rows = [...document.querySelectorAll("table.automations tbody tr")];
  assert.match(rows[0].textContent, /nightly-review/);
  assert.match(rows[0].textContent, /owner\/app/);
  assert.match(rows[0].textContent, /default/);
  assert.match(rows[0].textContent, /0 9 \* \* 1-5/);
  assert.match(rows[0].textContent, /UTC/);
  assert.match(rows[0].textContent, /Done/);
  assert.match(rows[0].textContent, /Reviewed 2 PRs\./);
  assert.equal(rows[0].querySelector("a.automation-last-run").getAttribute("href"), "#/jobs/t_last1");

  assert.match(rows[1].textContent, /weekly-deps/);
  assert.match(rows[1].textContent, /owner\/api/);
  assert.match(rows[1].textContent, /maintainer/);
  assert.match(rows[1].textContent, /Never/);

  // Clicking Run opens the prompt dialog pre-filled with the saved prompt.
  const firstRunButton = rows[0].querySelector(".automation-action button");
  assert.equal(firstRunButton.textContent, "Run");
  await act(async () => firstRunButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));

  await eventually(() => assert.ok(document.querySelector('[role="dialog"]')));
  const textarea = document.querySelector("#automation-run-prompt");
  assert.equal(textarea.value, "Review open pull requests and summarize blockers.");
  assert.equal(document.querySelector(".automation-prompt-edited"), null);

  // Submitting as-is posts without a prompt override and navigates to the created job.
  const runNowBtn = () => [...document.querySelectorAll('[role="dialog"] button[type="submit"]')][0];
  await act(async () => runNowBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));

  await eventually(() => assert.equal(runCalls.length, 1));
  assert.deepEqual(runCalls[0], { url: "/api/v1/automations/nightly-review/run", body: undefined });
  assert.equal(window.location.hash, "#/jobs/t_run_1");
  assert.equal(document.querySelector('[role="dialog"]'), null, "dialog closes after starting");

  // Reopen Run, edit the prompt, test Reset to saved, test blank validation, then run with edited prompt.
  window.location.hash = "#/jobs";
  await act(async () => firstRunButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.ok(document.querySelector("#automation-run-prompt")));
  assert.equal(document.querySelector("#automation-run-prompt").value, "Review open pull requests and summarize blockers.");

  await typeInto(document.querySelector("#automation-run-prompt"), "Focus only on security PRs.");
  assert.ok(document.querySelector(".automation-prompt-edited"), "shows edited indicator");

  // Reset to saved restores the original saved prompt.
  await act(async () => document.querySelector(".automation-reset").dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.equal(document.querySelector("#automation-run-prompt").value, "Review open pull requests and summarize blockers.");
  assert.equal(document.querySelector(".automation-prompt-edited"), null);

  // Blank prompt disables Run now.
  await typeInto(document.querySelector("#automation-run-prompt"), "   ");
  assert.equal(runNowBtn().disabled, true);

  // API error renders alert inside dialog and stays open.
  failNextRun = true;
  await typeInto(document.querySelector("#automation-run-prompt"), "Check only auth changes.");
  assert.equal(runNowBtn().disabled, false);
  await act(async () => runNowBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.match(document.querySelector('[role="dialog"] [role="alert"]')?.textContent || "", /prompt cannot be empty/));
  assert.equal(window.location.hash, "#/jobs");

  // Submitting again succeeds with the edited prompt override.
  await act(async () => runNowBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(runCalls.length, 3));
  assert.deepEqual(runCalls[2], { url: "/api/v1/automations/nightly-review/run", body: { prompt: "Check only auth changes." } });
  assert.equal(window.location.hash, "#/jobs/t_run_3");

  // Escape closes an open dialog without starting a run.
  await act(async () => firstRunButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.ok(document.querySelector('[role="dialog"]')));
  await act(async () => window.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  await eventually(() => assert.equal(document.querySelector('[role="dialog"]'), null));
  assert.equal(runCalls.length, 3);
});

test("Automations empty state explains how to add a markdown file under factory/automations/", { timeout: 30000 }, async context => {
  const { cleanup } = setupDOM("automations");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });

  globalThis.fetch = async url => {
    if (url === "/api/v1/automations") return { ok: true, status: 200, json: async () => ({ automations: [] }) };
    throw new Error(`unexpected ${url}`);
  };

  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  await act(async () => root.render(React.createElement(JobsPage, { runners: [], onSignedOut() {} })));

  await eventually(() => assert.ok(document.querySelector(".automations-empty")));
  const text = document.querySelector(".automations-empty").textContent;
  assert.match(text, /No automations configured/);
  assert.match(text, /factory\/automations\//);
  assert.match(text, /schedule:/);
  assert.match(text, /timezone:/);
  assert.match(text, /repo:/);
  assert.match(text, /preset:/);
  assert.equal(document.querySelector("table.automations"), null);
});
