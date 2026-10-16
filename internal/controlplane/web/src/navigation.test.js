import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("retired Inbox opens Runs and task actions remain available", { timeout: 15000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/inbox" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = false;
  const job = { id: "last-task", repository: "owner/repo", recipe: "factory", state_backend: "local", state: "blocked", task: { title: "Task needing attention" }, workflow: { name: "factory", steps: ["triage"], current_step: 0, factory: {} }, runs: [{ id: "triage-run", command: "triage", state: "succeeded", summary: "Needs a decision" }] };
  globalThis.fetch = async (url, options) => {
    if (url.endsWith("/cancel") && options?.method === "POST") { job.state = "cancelled"; return { ok: true, json: async () => ({}) }; }
    if (url.endsWith("/artifacts")) return { ok: true, json: async () => [] };
    return { ok: true, json: async () => ({ jobs: [{ ...job }], workers: [], commands: [], repositories: ["owner/repo"], triggers: [], state_backend: "local", csrf_token: "test" }) };
  };
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  let root;
  context.after(async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    await act(async () => root?.unmount());
    await server.close(); dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  root = (await server.ssrLoadModule("/src/main.jsx")).appRoot;
  await eventually(() => assert.ok(document.querySelector('[role="table"][aria-label="Runs"]')));
  assert.equal(document.querySelector("h1").textContent, "Runs");
  assert.equal(document.querySelector('nav a[href="#/inbox"]'), null);
  assert.match(document.querySelector('[role="table"]').textContent, /Task needing attention/);
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  await act(async () => document.querySelector('a[href="#/runs/last-task"]').click());
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Task needing attention"));
  const cancel = [...document.querySelectorAll("main button")].find(button => button.textContent === "Cancel task");
  assert.ok(cancel);
  await act(async () => cancel.click());
  await eventually(() => assert.equal([...document.querySelectorAll("main button")].some(button => button.textContent === "Cancel task"), false));
  await act(async () => { window.location.hash = "#/runs"; });
  await eventually(() => assert.ok(document.querySelector('[role="table"][aria-label="Runs"]')));
  assert.match(document.querySelector('[role="table"]').textContent, /Task needing attention/);
  assert.match(document.querySelector('[role="table"]').textContent, /Cancelled/);
});

async function eventually(check) {
  for (let i = 0; i < 30; i++) {
    try { check(); return; } catch (error) { if (i === 29) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 10)));
  }
}
