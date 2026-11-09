import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("sign in, assign work, then follow it on Work", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/assign" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  let signedIn = false;
  const ticket = { id: "t_new", state: "working", repo: "owner/repo", title: "Add dark mode", created_by: "alice", updated_at: new Date().toISOString() };
  const tickets = [];
  const posts = [];
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
    if (url === "/api/v1/tickets" && options.method === "POST") { posts.push(JSON.parse(options.body)); tickets.push(ticket); return reply(201, ticket); }
    if (url === "/api/v1/tickets") return reply(200, { tickets });
    if (url === "/api/v1/runners") return reply(200, { runners: [] });
    if (url === "/api/v1/automations") return reply(200, { automations: [] });
    if (url === "/api/v1/ledger") return reply(200, { merged_prs: 0 });
    if (url === "/api/v1/tickets/t_new") return reply(200, { ticket, entries: [], runs: [], figures: { questions: 0, feedback_rounds: 0, tokens: 0, elapsed: 0, merged: false } });
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

  await eventually(() => assert.equal(document.querySelector("h1").textContent, "What needs doing?"));
  assert.deepEqual([...document.querySelectorAll("nav a")].map(link => link.textContent), ["Assign", "Work", "Automations", "Runners"]);
  await type("#assign-work", "Add dark mode");
  await act(async () => document.querySelector("form button").click());
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Add dark mode"));
  assert.equal(window.location.hash, "#/work/t_new");
  assert.equal(posts[0].repo, "owner/repo");
  assert.equal(posts[0].body, "Add dark mode");
  assert.ok(posts[0].request_id);

  await act(async () => { window.location.hash = "#/work"; });
  await eventually(() => assert.match(document.querySelector("main").textContent, /Waiting on an agent/));
  assert.ok(document.querySelector('a[href="#/work/t_new"]'));
  assert.equal(document.querySelector('nav a[aria-current="page"]').textContent, "Work");

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

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}
