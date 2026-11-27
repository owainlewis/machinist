import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

function setupDOM(url = "http://localhost/#/automations") {
  const dom = new JSDOM('<div id="root"></div>', { url });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "MouseEvent", "KeyboardEvent", "Event", "localStorage", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
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

async function setControlValue(element, value) {
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "value").set;
  const eventName = element.tagName === "SELECT" ? "change" : "input";
  await act(async () => {
    setter.call(element, value);
    element.dispatchEvent(new window.Event(eventName, { bubbles: true }));
  });
}

test("Jobs page shows only Board and List (Automations tab is removed)", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM("http://localhost/#/jobs");
  dom.window.localStorage.setItem("machinist-jobs-view", "automations");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });

  globalThis.fetch = async url => {
    if (url.startsWith("/api/v1/tickets")) {
      return { ok: true, status: 200, json: async () => ({ tickets: [], total: 0, limit: 20, offset: 0, has_more: false, done_total: 0, done_limit: 20 }) };
    }
    throw new Error(`unexpected ${url}`);
  };

  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  await act(async () => root.render(React.createElement(JobsPage, { runners: [], onSignedOut() {} })));

  await eventually(() => assert.ok(document.querySelector(".range")));
  assert.deepEqual([...document.querySelectorAll(".range button")].map(btn => btn.textContent), ["Board", "List"]);
  assert.equal(document.querySelector("table.automations"), null);
});

test("Automations page lists file and UI automations, creates/edits/deletes, and runs with overrides opening the job", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM("http://localhost/#/automations");
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
      editable: false,
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
      name: "manual-codex-audit",
      repo: "owner/api",
      agent: "default",
      harness: "codex",
      model: "gpt-5-codex",
      runs_on: ["gpu", "vpc"],
      schedule: "",
      timezone: "",
      prompt: "Audit outdated dependencies.",
      editable: true,
    },
  ];

  const runCalls = [];
  const createCalls = [];
  const updateCalls = [];
  const deleteCalls = [];
  let failNextRun = false;

  globalThis.fetch = async (url, options = {}) => {
    const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
    if (url === "/api/v1/repositories") {
      return reply(200, { repositories: ["owner/app", "owner/api"] });
    }
    if (url === "/api/v1/presets") {
      return reply(200, { default: "default", presets: [{ name: "default" }, { name: "neon" }] });
    }
    if (url === "/api/v1/automations" && (!options.method || options.method === "GET")) {
      return reply(200, { automations });
    }
    if (url === "/api/v1/automations" && options.method === "POST") {
      const body = JSON.parse(options.body);
      createCalls.push(body);
      automations = [...automations, { ...body, agent: body.preset, editable: true }];
      return reply(201, body);
    }
    if (url.startsWith("/api/v1/automations/") && options.method === "PUT") {
      const name = decodeURIComponent(url.slice("/api/v1/automations/".length));
      const body = JSON.parse(options.body);
      updateCalls.push({ name, body });
      automations = automations.map(item => (item.name === name ? { ...item, ...body, agent: body.preset } : item));
      return reply(200, { name, ...body });
    }
    if (url.startsWith("/api/v1/automations/") && options.method === "DELETE") {
      const name = decodeURIComponent(url.slice("/api/v1/automations/".length));
      deleteCalls.push(name);
      automations = automations.filter(item => item.name !== name);
      return { ok: true, status: 204, json: async () => null };
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

  const { AutomationsPage } = await server.ssrLoadModule("/src/automations-view.jsx");
  const runners = [{ name: "vm-runner", harnesses: ["steps", "claude", "codex"] }];
  await act(async () => root.render(React.createElement(AutomationsPage, { runners, onSignedOut() {} })));

  await eventually(() => assert.equal(document.querySelectorAll("table.automations tbody tr").length, 2));
  assert.equal(document.querySelector("h1").textContent, "Automations");

  const rows = [...document.querySelectorAll("table.automations tbody tr")];
  // Row 0: file-based scheduled automation (no Edit/Delete buttons).
  assert.match(rows[0].textContent, /nightly-review/);
  assert.match(rows[0].textContent, /File/);
  assert.match(rows[0].textContent, /0 9 \* \* 1-5/);
  assert.match(rows[0].textContent, /Done/);
  assert.equal(rows[0].querySelector("a.automation-last-run").getAttribute("href"), "#/jobs/t_last1");
  assert.equal(rows[0].querySelector('button[aria-label="Edit nightly-review"]'), null);

  // Row 1: UI-managed manual automation with Codex override.
  assert.match(rows[1].textContent, /manual-codex-audit/);
  assert.match(rows[1].textContent, /Codex · gpt-5-codex/);
  assert.match(rows[1].textContent, /Manual/);
  assert.ok(rows[1].querySelector('button[aria-label="Edit manual-codex-audit"]'));
  assert.ok(rows[1].querySelector('button[aria-label="Delete manual-codex-audit"]'));

  // Create a new manual automation via the dialog.
  const newBtn = [...document.querySelectorAll("header button")].find(btn => btn.textContent.includes("New automation"));
  await act(async () => newBtn.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.ok(document.querySelector("#automation-edit-name")));
  await setControlValue(document.querySelector("#automation-edit-name"), "quick-lint");
  await setControlValue(document.querySelector("#automation-edit-harness"), "codex");
  await setControlValue(document.querySelector("#automation-edit-model"), "gpt-5.3-codex");
  await setControlValue(document.querySelector("#automation-edit-prompt"), "Fix lint issues.");
  await act(async () => document.querySelector('[role="dialog"] button[type="submit"]').dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));

  await eventually(() => assert.equal(createCalls.length, 1));
  assert.deepEqual(createCalls[0], {
    name: "quick-lint",
    repo: "owner/app",
    preset: "default",
    harness: "codex",
    model: "gpt-5.3-codex",
    schedule: "",
    timezone: "",
    prompt: "Fix lint issues.",
  });
  await eventually(() => assert.equal(document.querySelectorAll("table.automations tbody tr").length, 3));

  // Click Run on nightly-review -> opens modal pre-filled; submit as-is -> navigates to normal job route.
  const firstRunButton = document.querySelector('button[aria-label="Run nightly-review"]');
  await act(async () => firstRunButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.ok(document.querySelector("#automation-run-prompt")));
  assert.equal(document.querySelector("#automation-run-prompt").value, "Review open pull requests and summarize blockers.");

  const runNowBtn = () => document.querySelector('[role="dialog"] button[type="submit"]');
  await act(async () => runNowBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(runCalls.length, 1));
  assert.deepEqual(runCalls[0], { url: "/api/v1/automations/nightly-review/run", body: undefined });
  assert.equal(window.location.hash, "#/jobs/t_run_1");

  // Reopen Run, override repo + preset + harness + model + prompt, verify Reset and submit override.
  window.location.hash = "#/automations";
  await act(async () => firstRunButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.ok(document.querySelector("#automation-run-harness")));
  await setControlValue(document.querySelector("#automation-run-repo"), "owner/api");
  await setControlValue(document.querySelector("#automation-run-preset"), "neon");
  await setControlValue(document.querySelector("#automation-run-harness"), "codex");
  await setControlValue(document.querySelector("#automation-run-model"), "gpt-5-codex");
  await setControlValue(document.querySelector("#automation-run-prompt"), "Focus only on security PRs.");
  assert.ok(document.querySelector(".automation-prompt-edited"));

  // Error keeps dialog open; retry succeeds and opens the job.
  failNextRun = true;
  await act(async () => runNowBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.match(document.querySelector('[role="dialog"] [role="alert"]')?.textContent || "", /prompt cannot be empty/));

  await act(async () => runNowBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(runCalls.length, 3));
  assert.deepEqual(runCalls[2], {
    url: "/api/v1/automations/nightly-review/run",
    body: {
      repo: "owner/api",
      preset: "neon",
      harness: "codex",
      model: "gpt-5-codex",
      prompt: "Focus only on security PRs.",
    },
  });
  assert.equal(window.location.hash, "#/jobs/t_run_3");

  // Edit UI-managed automation and verify runs_on labels are preserved on PUT.
  const editBtn = document.querySelector('button[aria-label="Edit manual-codex-audit"]');
  await act(async () => editBtn.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.ok(document.querySelector("#automation-edit-prompt")));
  await setControlValue(document.querySelector("#automation-edit-prompt"), "Audit outdated and vulnerable dependencies.");
  await act(async () => document.querySelector('[role="dialog"] button[type="submit"]').dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(updateCalls.length, 1));
  assert.deepEqual(updateCalls[0], {
    name: "manual-codex-audit",
    body: {
      repo: "owner/api",
      preset: "default",
      harness: "codex",
      model: "gpt-5-codex",
      schedule: "",
      timezone: "",
      prompt: "Audit outdated and vulnerable dependencies.",
      runs_on: ["gpu", "vpc"],
    },
  });

  // Delete UI-managed automation with inline confirmation.
  const delBtn = document.querySelector('button[aria-label="Delete manual-codex-audit"]');
  await act(async () => delBtn.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  const confirmBtn = [...document.querySelectorAll('tr[data-automation="manual-codex-audit"] button')].find(btn => btn.textContent === "Delete");
  await act(async () => confirmBtn.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.deepEqual(deleteCalls, ["manual-codex-audit"]));
});

test("Automations empty state offers New automation and documents factory/automations/", { timeout: 30000 }, async context => {
  const { cleanup } = setupDOM("http://localhost/#/automations");
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

  const { AutomationsPage } = await server.ssrLoadModule("/src/automations-view.jsx");
  await act(async () => root.render(React.createElement(AutomationsPage, { runners: [], onSignedOut() {} })));

  await eventually(() => assert.ok(document.querySelector(".automations-empty")));
  const text = document.querySelector(".automations-empty").textContent;
  assert.match(text, /No automations yet/);
  assert.match(text, /factory\/automations\//);
  assert.equal(document.querySelector("table.automations"), null);
});
