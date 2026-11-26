import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("Runners: removing an offline runner asks first, then drops it from the fleet", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/runners" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "getComputedStyle", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  const now = new Date().toISOString();
  let runners = [
    { name: "laptop", kind: "local", last_contact: now, jobs: [] },
    { name: "old-laptop", kind: "local", last_contact: "2026-01-01T00:00:00Z", jobs: [] },
  ];
  const removed = [];
  const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/v1/auth/status") return reply(200, { authenticated: true, methods: [], token_signin_enabled: true });
    if (url === "/api/v1/me") return reply(200, { auth_method: "token", role: "admin", version: "v1.0.0", token_name: "alice" });
    if (url === "/api/v1/repositories") return reply(200, { repositories: [], name: "alice" });
    if (url === "/api/v1/setup") return reply(200, { ready: true, checks: [], projects: [] });
    if (url === "/api/v1/tickets") return reply(200, { tickets: [] });
    if (url === "/api/v1/runners") return reply(200, { runners, queue: { waiting: 0 } });
    if (url.startsWith("/api/v1/runners/") && options.method === "DELETE") {
      const name = decodeURIComponent(url.slice("/api/v1/runners/".length));
      removed.push(name);
      runners = runners.filter(runner => runner.name !== name);
      return reply(200, { runners, queue: { waiting: 0 } });
    }
    return reply(404, { error: `no route ${url}` });
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

  await eventually(() => assert.match(document.querySelector(".page-sub")?.textContent || "", /1 of 2 online/));
  assert.equal(document.querySelector('[aria-label="Remove laptop"]'), null, "an online runner cannot be removed");
  await act(async () => document.querySelector('[aria-label="Remove old-laptop"]').click());
  assert.deepEqual(removed, [], "removing asks first");
  assert.match(document.querySelector('[aria-label="Remove old-laptop?"]').textContent, /comes back if it asks for work again/);
  await act(async () => [...document.querySelectorAll('[aria-label="Remove old-laptop?"] button')].find(button => button.textContent === "Remove").click());
  await eventually(() => assert.match(document.querySelector(".page-sub").textContent, /1 of 1 online/));
  assert.deepEqual(removed, ["old-laptop"]);
});

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}
