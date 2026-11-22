import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const check = (name, ok, required = true) => ({ name, ok, required, detail: `${name} detail`, fix: ok ? "" : `Fix ${name}` });

test("Settings: a new install opens on the overview, each page has a route, and projects and tokens change", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "getComputedStyle", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  let projects = [];
  let runner = false;
  let signedIn = true;
  const added = [];
  const revoked = [];
  const setup = () => ({
    ready: projects.length > 0,
    checks: [check("GitHub", true), check("Runner", runner), check("Projects", projects.length > 0), check("Linear", false, false)],
    projects,
    install_url: "https://github.com/apps/machinistagent/installations/new",
  });
  const now = new Date().toISOString();
  let tokens = [
    { name: "alice", role: "operator", created_at: "2026-11-01T12:00:00Z", last_used_at: now, current: true },
    { name: "vm-runner", role: "runner", created_at: "2026-11-02T12:00:00Z" },
    { name: "old", role: "all", created_at: "2026-10-01T12:00:00Z", revoked_at: "2026-10-05T12:00:00Z" },
  ];
  const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
  globalThis.fetch = async (url, options = {}) => {
    if (!signedIn) return reply(401, { error: "a token is required" });
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
    if (url === "/api/v1/settings/github") return reply(200, {
      handle: "machinist-bot", auto_merge: true, app: { id: 42, name: "machinistagent[bot]", install_url: "https://github.com/apps/machinistagent/installations/new" },
      repositories: [
        { repo: "owner/web", default_branch: "main", protected: true, admin: false, warnings: [] },
        { repo: "owner/api", default_branch: "trunk", protected: false, admin: true, warnings: ["trunk has no branch protection, and auto-merge is on.", "The login is an admin here."] },
      ],
    });
    if (url === "/api/v1/settings/linear") return reply(200, { enabled: false, allowed_users: [], teams: [], token_env: "LINEAR_API_TOKEN", docs: "https://example.com/linear.md" });
    if (url === "/api/v1/tokens") return reply(200, { tokens });
    if (url.startsWith("/api/v1/tokens/") && options.method === "DELETE") {
      const name = decodeURIComponent(url.slice("/api/v1/tokens/".length));
      revoked.push(name);
      if (name === "alice") { signedIn = false; return { ok: true, status: 204, json: async () => null }; }
      tokens = tokens.map(token => token.name === name ? { ...token, revoked_at: now } : token);
      return reply(200, { tokens });
    }
    if (url === "/api/v1/presets") return reply(200, { default: "default", presets: [{ name: "default", description: "Implement and open a pull request.", steps: [{ name: "checkout", agents: [] }, { name: "implement", agents: [{ name: "implement", harness: "claude", model: "opus" }] }] }] });
    if (url === "/api/v1/agents") return reply(200, { agents: [{ name: "implement", harness: "claude", model: "opus", presets: ["default"] }, { name: "review", harness: "codex", presets: [] }] });
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

  const heading = () => document.querySelector("h1")?.textContent;
  const marked = () => document.querySelector('.settings-nav a[aria-current="page"]')?.textContent;
  const go = async hash => act(async () => {
    window.location.hash = hash;
    window.dispatchEvent(new window.HashChangeEvent("hashchange"));
  });

  // A new install opens on the overview until every required step is done.
  await eventually(() => assert.equal(window.location.hash, "#/settings/overview"));
  window.dispatchEvent(new window.HashChangeEvent("hashchange"));
  await eventually(() => assert.equal(heading(), "Overview"));
  assert.equal(marked(), "Overview");
  assert.equal(document.querySelector('nav[aria-label="Primary"] a[aria-current="page"]').textContent, "Settings");
  assert.deepEqual([...document.querySelectorAll(".settings-nav a")].map(link => link.getAttribute("href")), ["#/settings/overview", "#/settings/projects", "#/settings/github", "#/settings/linear", "#/settings/tokens", "#/settings/presets"]);
  await eventually(() => assert.match(document.querySelector(".page-sub").textContent, /1 of 3 required steps/));
  assert.ok(document.querySelector('[aria-label="Settings need attention"]'));
  assert.ok(document.querySelector('a[aria-label="Open Projects settings"][href="#/settings/projects"]'));

  // A runner started elsewhere shows up when the window comes back into focus.
  runner = true;
  await act(async () => window.dispatchEvent(new window.Event("focus")));
  await eventually(() => assert.match(document.querySelector(".page-sub").textContent, /2 of 3 required steps/));
  assert.match(document.querySelector(".setup-checks").textContent, /Fix Projects/);

  // The old setup page redirects to the overview.
  await go("#/jobs");
  await eventually(() => assert.equal(heading(), "Jobs"));
  await go("#/setup");
  await eventually(() => assert.equal(window.location.hash, "#/settings/overview"));
  await eventually(() => assert.equal(heading(), "Overview"));

  // Projects: add and remove.
  await go("#/settings/projects");
  await eventually(() => assert.equal(heading(), "Projects"));
  assert.equal(marked(), "Projects");
  await eventually(() => assert.ok(document.querySelector("#project-repo")));
  await type("#project-repo", "not a repo");
  assert.equal(document.querySelector(".project-add button").disabled, true);
  await type("#project-repo", "owner/missing");
  await act(async () => document.querySelector(".project-add button").click());
  await eventually(() => assert.match(document.querySelector('[role="alert"]').textContent, /not installed on owner\/missing/));
  await type("#project-repo", "https://github.com/owner/web");
  await act(async () => document.querySelector(".project-add button").click());
  await eventually(() => assert.match(document.querySelector(".project-row")?.textContent || "", /owner\/webAdded by alice/));
  assert.deepEqual(added, ["owner/missing", "owner/web"]);
  assert.equal(document.querySelector('[aria-label="Settings need attention"]'), null);
  await act(async () => document.querySelector('[aria-label="Remove owner/web"]').click());
  await eventually(() => assert.match(document.querySelector(".empty").textContent, /No projects yet/));

  // GitHub: the app, auto-merge and a warning for each unprotected branch.
  await go("#/settings/github");
  await eventually(() => assert.equal(heading(), "GitHub"));
  assert.equal(marked(), "GitHub");
  await eventually(() => assert.match(document.querySelector(".settings-facts")?.textContent || "", /machinistagent\[bot\]/));
  assert.match(document.querySelector(".page-sub").textContent, /1 repository needs a look/);
  assert.match(document.querySelector(".settings-facts").textContent, /Auto-merge.*On/);
  assert.deepEqual([...document.querySelectorAll(".settings-row .pill")].map(pill => pill.textContent), ["Protected", "Not protected"]);
  assert.equal(document.querySelectorAll(".settings-warnings li").length, 2);

  // Linear: off, and how to turn it on.
  await go("#/settings/linear");
  await eventually(() => assert.equal(heading(), "Linear"));
  assert.equal(marked(), "Linear");
  await eventually(() => assert.match(document.querySelector(".settings-howto")?.textContent || "", /LINEAR_API_TOKEN/));

  // Presets and agents: read only.
  await go("#/settings/presets");
  await eventually(() => assert.equal(heading(), "Presets and agents"));
  assert.equal(marked(), "Presets and agents");
  await eventually(() => assert.deepEqual([...document.querySelectorAll(".preset-step-list .step-name")].map(name => name.textContent), ["checkout", "implement"]));
  assert.match(document.querySelector(".agent-row").textContent, /Claude Code.*opus.*default/);
  assert.match(document.querySelectorAll(".agent-row")[1].textContent, /Codex default.*No preset/);
  assert.equal(document.querySelector(".settings-body button"), null, "nothing here changes the factory");

  // Tokens: list, then revoke one, and revoking your own signs you out.
  await go("#/settings/tokens");
  await eventually(() => assert.equal(heading(), "Tokens"));
  assert.equal(marked(), "Tokens");
  await eventually(() => assert.equal(document.querySelectorAll(".token-row").length, 3));
  assert.match(document.querySelector(".page-sub").textContent, /2 active tokens, 1 revoked/);
  assert.match(document.querySelector(".token-row").textContent, /alice.*You.*Person/);
  assert.equal(document.querySelector('[aria-label="Revoke old"]'), null, "a revoked token cannot be revoked again");
  await act(async () => document.querySelector('[aria-label="Revoke vm-runner"]').click());
  assert.deepEqual(revoked, [], "revoking asks first");
  assert.match(document.querySelector('[aria-label="Revoke vm-runner?"]').textContent, /Its runner stops/);
  await act(async () => [...document.querySelectorAll('[aria-label="Revoke vm-runner?"] button')].find(button => button.textContent === "Revoke").click());
  await eventually(() => assert.equal(document.querySelectorAll('.token-row[data-revoked]').length, 2));
  assert.deepEqual(revoked, ["vm-runner"]);
  await act(async () => document.querySelector('[aria-label="Revoke alice"]').click());
  await act(async () => [...document.querySelectorAll('[aria-label="Revoke alice?"] button')].find(button => button.textContent === "Revoke").click());
  await eventually(() => assert.ok(document.querySelector("#token"), "signed out"));
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
