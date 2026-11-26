import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}

async function mountApp(context, { url, fetchImpl }) {
  const dom = new JSDOM('<div id="root"></div>', { url });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "getComputedStyle", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.fetch = fetchImpl;
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
}

const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });

test("Viewer UI hides write/admin controls, skips /api/v1/setup, and opens My Account in Settings", { timeout: 30000 }, async context => {
  let fetchedSetup = false;
  await mountApp(context, {
    url: "http://localhost/#/new",
    fetchImpl: async url => {
      if (url === "/api/v1/auth/status") return reply(200, { authenticated: true, methods: ["github"], token_signin_enabled: false });
      if (url === "/api/v1/me") return reply(200, { auth_method: "github", role: "viewer", version: "v1.0.0", user: { id: "usr_1", github_login: "obs", name: "Observer", email: "obs@example.com", avatar_url: "" } });
      if (url === "/api/v1/setup") { fetchedSetup = true; return reply(403, { error: "forbidden" }); }
      if (url === "/api/v1/runners") return reply(200, { runners: [{ name: "old-runner", kind: "local", last_contact: "2026-01-01T00:00:00Z", jobs: [] }], queue: { waiting: 0 } });
      if (url.startsWith("/api/v1/tickets")) return reply(200, { tickets: [], total: 0, limit: 50, offset: 0, has_more: false, done_total: 0, done_limit: 20 });
      if (url === "/api/v1/me/sessions") return reply(200, { sessions: [{ id: "ses_1", auth_method: "github", user_agent: "Chrome", ip_address: "127.0.0.1", created_at: "2026-11-26T12:00:00Z", last_seen_at: "2026-11-26T12:00:00Z", current: true }] });
      return reply(404, { error: `unexpected ${url}` });
    },
  });

  // Viewer visiting #/new is redirected to #/jobs and sees no "New job" button.
  await eventually(() => assert.equal(window.location.hash, "#/jobs"));
  await eventually(() => assert.equal(document.querySelector("h1")?.textContent, "Jobs"));
  assert.equal(fetchedSetup, false, "viewer must not call /api/v1/setup");
  assert.equal(document.querySelector('a[href="#/new"]'), null, "viewer must not see New job or Start one link");
  assert.match(document.querySelector(".sidebar-user")?.textContent || "", /@obs/);

  // Viewer visiting #/runners sees no Remove button on offline runner.
  await act(async () => {
    window.location.hash = "#/runners";
    window.dispatchEvent(new window.HashChangeEvent("hashchange"));
  });
  await eventually(() => assert.equal(document.querySelector("h1")?.textContent, "Runners"));
  assert.equal(document.querySelector('button[aria-label="Remove old-runner"]'), null, "viewer must not see Remove runner");

  // Viewer visiting #/settings lands on My Account and only sees Presets + My Account tabs.
  await act(async () => {
    window.location.hash = "#/settings";
    window.dispatchEvent(new window.HashChangeEvent("hashchange"));
  });
  await eventually(() => assert.equal(document.querySelector("h1")?.textContent, "My Account"));
  assert.deepEqual([...document.querySelectorAll(".settings-nav a")].map(a => a.getAttribute("href")), ["#/settings/presets", "#/settings/account"]);
  await eventually(() => assert.match(document.querySelector(".session-list")?.textContent || "", /Chrome/));
});

test("Admin Settings -> Team lists members, guards sole admin, and creates one-time fragment invite", { timeout: 30000 }, async context => {
  const members = [
    { id: "usr_1", role: "admin", github_login: "boss", name: "Boss", email: "boss@team.example", avatar_url: "", disabled_at: null, last_seen_at: "2026-11-26T12:00:00Z", created_at: "2026-11-01T00:00:00Z" },
    { id: "usr_2", role: "member", github_login: "dev", name: "Dev", email: "dev@team.example", avatar_url: "", disabled_at: null, last_seen_at: "2026-11-26T11:00:00Z", created_at: "2026-11-02T00:00:00Z" },
  ];
  let invites = [];

  await mountApp(context, {
    url: "http://localhost/#/settings/team",
    fetchImpl: async (url, options = {}) => {
      if (url === "/api/v1/auth/status") return reply(200, { authenticated: true, methods: ["github"], token_signin_enabled: false });
      if (url === "/api/v1/me") return reply(200, { auth_method: "github", role: "admin", version: "v1.0.0", user: { id: "usr_1", github_login: "boss", name: "Boss", email: "boss@team.example", avatar_url: "" } });
      if (url === "/api/v1/setup") return reply(200, { ready: true, checks: [], projects: [] });
      if (url === "/api/v1/runners") return reply(200, { runners: [], queue: { waiting: 0 } });
      if (url === "/api/v1/team/members") return reply(200, { members });
      if (url === "/api/v1/team/invites" && (!options.method || options.method === "GET")) return reply(200, { invites });
      if (url === "/api/v1/team/invites" && options.method === "POST") {
        const body = JSON.parse(options.body);
        const inv = { id: "inv_1", role: body.role, github_login: body.github_login, created_by: "usr_1", expires_at: "2026-12-03T00:00:00Z", created_at: "2026-11-26T12:00:00Z" };
        invites = [inv];
        return reply(201, { invite: inv, invite_url: "https://machinist.example.com/#/invite#minv_onetime" });
      }
      return reply(404, { error: `unexpected ${url}` });
    },
  });

  await eventually(() => assert.equal(document.querySelector("h1")?.textContent, "Team"));
  await eventually(() => assert.ok(document.querySelector('tr[data-member="boss"]')));
  // Sole admin role dropdown and Disable button are disabled.
  assert.equal(document.querySelector('select[aria-label="Role for boss"]').disabled, true);
  assert.equal(document.querySelector('select[aria-label="Role for dev"]').disabled, false);

  // Create invite and check one-time fragment banner.
  const input = document.querySelector("#invite-target-value");
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input), "value").set;
  await act(async () => {
    setter.call(input, "newdev");
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
  await act(async () => {
    document.querySelector(".team-invite-form button[type='submit']").click();
  });
  await eventually(() => assert.match(document.querySelector(".team-invite-url")?.textContent || "", /#\/invite#minv_onetime/));
  await eventually(() => assert.ok(document.querySelector('tr[data-invite="inv_1"]')));
});
