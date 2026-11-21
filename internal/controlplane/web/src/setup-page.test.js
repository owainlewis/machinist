import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const check = (name, ok, required = true) => ({ name, ok, required, detail: `${name} detail`, fix: ok ? "" : `Fix ${name}` });

test("a new install opens on setup, and adding a project finishes it", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "getComputedStyle", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  let projects = [];
  const added = [];
  const setup = () => ({
    ready: projects.length > 0,
    checks: [check("GitHub", true), check("Runner", true), check("Projects", projects.length > 0), check("Linear", false, false)],
    projects,
    install_url: "https://github.com/apps/machinistagent/installations/new",
  });
  const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/v1/repositories") return reply(200, { repositories: [], name: "alice" });
    if (url === "/api/v1/setup") return reply(200, setup());
    if (url === "/api/v1/projects" && options.method === "POST") {
      const { repo } = JSON.parse(options.body);
      added.push(repo);
      if (repo === "owner/missing") return reply(400, { error: "the GitHub App is not installed on owner/missing" });
      projects = [{ repo, source: "app", added_by: "alice" }];
      return reply(200, setup());
    }
    if (url === "/api/v1/projects/owner/web" && options.method === "DELETE") { projects = []; return reply(200, setup()); }
    if (url === "/api/v1/tickets") return reply(200, { tickets: [] });
    if (url === "/api/v1/runners") return reply(200, { runners: [], queue: { waiting: 0 } });
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

  await eventually(() => assert.equal(window.location.hash, "#/setup"));
  window.dispatchEvent(new window.HashChangeEvent("hashchange"));
  await eventually(() => assert.equal(document.querySelector("h1")?.textContent, "Setup"));
  await eventually(() => assert.match(document.querySelector(".page-sub").textContent, /2 of 3 required steps/));
  assert.ok(document.querySelector('[aria-label="Setup needs attention"]'));
  assert.match(document.querySelector(".setup-checks").textContent, /Fix Projects/);

  await type("#project-repo", "not a repo");
  assert.equal(document.querySelector(".project-add button").disabled, true);
  await type("#project-repo", "owner/missing");
  await act(async () => document.querySelector(".project-add button").click());
  await eventually(() => assert.match(document.querySelector('[role="alert"]').textContent, /not installed on owner\/missing/));

  await type("#project-repo", "https://github.com/owner/web");
  await act(async () => document.querySelector(".project-add button").click());
  await eventually(() => assert.match(document.querySelector(".page-sub").textContent, /ready to take jobs/));
  assert.deepEqual(added, ["owner/missing", "owner/web"]);
  assert.match(document.querySelector(".project-row").textContent, /owner\/webAdded by alice/);
  assert.equal(document.querySelector('[aria-label="Setup needs attention"]'), null);

  await act(async () => document.querySelector('[aria-label="Remove owner/web"]').click());
  await eventually(() => assert.match(document.querySelector(".empty").textContent, /No projects yet/));
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
