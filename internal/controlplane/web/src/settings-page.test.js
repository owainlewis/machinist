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
  let linear = false;
  let signedIn = true;
  let factoryUpdates = [];
  const added = [];
  const created = [];
  const regenerated = [];
  const revoked = [];
  let copiedText = "";
  Object.defineProperty(dom.window.navigator, "clipboard", {
    configurable: true,
    value: { writeText: async value => { copiedText = value; } },
  });
  const setup = () => ({
    ready: projects.length > 0,
    checks: [
      check("GitHub", true),
      check("Runner", runner),
      check("Projects", projects.length > 0),
      check("Linear", false, false),
      ...(factoryUpdates.length ? [{
        name: "Factory",
        ok: false,
        required: false,
        warning: true,
        detail: `${factoryUpdates.length} factory files are out of date: ${factoryUpdates.map(file => file.path).join(", ")}.`,
        fix: "Run machinist factory upgrade and restart the control plane.",
      }] : []),
    ],
    projects,
    install_url: "https://github.com/apps/machinistagent/installations/new",
    ...(factoryUpdates.length ? { factory_updates: factoryUpdates } : {}),
  });
  const now = new Date().toISOString();
  let tokens = [
    { name: "alice", role: "operator", prefix: "mch_1a2b", created_at: "2026-11-01T12:00:00Z", last_used_at: now, current: true },
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
    if (url === "/api/v1/settings/linear") return reply(200, linear
      ? { enabled: true, app_user_id: "app-1", allowed_users: ["alice@example.com"], preset: "default", teams: [], poll_seconds: 60, max_jobs_per_hour: 10, credential_env: ["LINEAR_CLIENT_ID", "LINEAR_CLIENT_SECRET"], renews: true, docs: "https://example.com/linear.md#set-it-up" }
      : { enabled: false, allowed_users: [], teams: [], credential_env: ["LINEAR_CLIENT_ID", "LINEAR_CLIENT_SECRET"], renews: true, docs: "https://example.com/linear.md#set-it-up" });
    if (url === "/api/v1/tokens" && (!options.method || options.method === "GET")) return reply(200, { tokens });
    if (url === "/api/v1/tokens" && options.method === "POST") {
      const { name, role } = JSON.parse(options.body);
      created.push({ name, role });
      if (tokens.some(token => token.name === name && !token.revoked_at)) return reply(409, { error: "an active token with this name exists" });
      const secret = `mch_abcd${"0".repeat(60)}`;
      const prefix = secret.slice(0, 8);
      tokens = [...tokens, { name, role, prefix, created_at: now }];
      return reply(201, { token: secret, name, role, prefix, tokens });
    }
    if (url.startsWith("/api/v1/tokens/") && url.endsWith("/regenerate") && options.method === "POST") {
      const name = decodeURIComponent(url.slice("/api/v1/tokens/".length, -"/regenerate".length));
      regenerated.push(name);
      const existing = tokens.find(token => token.name === name && !token.revoked_at);
      if (!existing) return reply(404, { error: `no active token is named ${name}` });
      const secret = `mch_ef99${"1".repeat(60)}`;
      const prefix = secret.slice(0, 8);
      tokens = [
        ...tokens.map(token => token === existing ? { ...token, revoked_at: now, current: false } : token),
        { name, role: existing.role, prefix, created_at: now, current: existing.current },
      ];
      return reply(200, { token: secret, name, role: existing.role, prefix, tokens });
    }
    if (url.startsWith("/api/v1/tokens/") && options.method === "DELETE") {
      const name = decodeURIComponent(url.slice("/api/v1/tokens/".length));
      revoked.push(name);
      if (name === "alice") { signedIn = false; return { ok: true, status: 204, json: async () => null }; }
      tokens = tokens.map(token => token.name === name && !token.revoked_at ? { ...token, revoked_at: now } : token);
      return reply(200, { tokens });
    }
    if (url === "/api/v1/presets") return reply(200, {
      default: "default",
      presets: [
        {
          name: "default",
          description: "Implement and open a pull request.",
          timeout_millis: 3600000,
          steps: [
            { name: "checkout", run: "checkout.sh", scripts: [{ path: "scripts/checkout.sh", content: "#!/bin/sh\ngit clone repo\n" }], timeout_millis: 300000, agents: [] },
            { name: "implement", run: "machinist agent implement", agents: [{ name: "implement", harness: "claude", model: "opus" }] },
            { name: "review", run: "review.sh", scripts: [{ path: "scripts/review.sh", content: "#!/bin/sh\nmachinist agent review\n" }], agents: [{ name: "review", harness: "claude", model: "sonnet" }] },
          ],
        },
        {
          name: "neon",
          description: "Default pipeline on Antigravity.",
          extends: "default",
          harness: "agy",
          model: "gemini-4-neon-high",
          timeout_millis: 3600000,
          steps: [
            { name: "checkout", run: "checkout.sh", scripts: [{ path: "scripts/checkout.sh", content: "#!/bin/sh\ngit clone repo\n" }], timeout_millis: 300000, agents: [] },
            { name: "implement", run: "machinist agent implement", agents: [{ name: "implement", harness: "agy", model: "gemini-4-neon-high" }] },
            { name: "review", run: "review.sh", scripts: [{ path: "scripts/review.sh", content: "#!/bin/sh\nmachinist agent review\n" }], agents: [{ name: "review", harness: "claude", model: "opus" }] },
          ],
        },
      ],
      ...(factoryUpdates.length ? { updates: factoryUpdates } : {}),
    });
    if (url === "/api/v1/agents") return reply(200, {
      agents: [
        {
          name: "implement",
          harness: "claude",
          model: "opus",
          prompt: "Implement the requested change with tests.",
          presets: ["default", "neon"],
          uses: [
            { preset: "default", step: "implement", harness: "claude", model: "opus" },
            { preset: "neon", step: "implement", harness: "agy", model: "gemini-4-neon-high" },
          ],
        },
        {
          name: "review",
          harness: "claude",
          model: "sonnet",
          prompt: "Review the diff carefully.",
          presets: ["default", "neon"],
          uses: [
            { preset: "default", step: "review", harness: "claude", model: "sonnet" },
            { preset: "neon", step: "review", harness: "claude", model: "opus" },
          ],
        },
        {
          name: "implement-legacy",
          harness: "codex",
          model: "gpt-5",
          prompt: "Implement the requested change with tests.",
          presets: ["legacy"],
          uses: [
            { preset: "legacy", step: "implement", harness: "codex", model: "gpt-5" },
          ],
        },
        {
          name: "spec-writer",
          harness: "agy",
          prompt: "Write a detailed specification.",
          presets: ["spec"],
          uses: [
            { preset: "spec", step: "write", harness: "agy" },
          ],
        },
        {
          name: "unused-codex",
          harness: "codex",
          prompt: "Legacy unused instructions.",
          presets: [],
          uses: [],
        },
      ],
    });
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
  assert.ok(document.querySelector('a[aria-label="Open Runners"][href="#/runners"]'), "the runner check links to the runners page");

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
  await eventually(() => assert.match(document.querySelector(".settings-howto")?.textContent || "", /LINEAR_CLIENT_ID and LINEAR_CLIENT_SECRET/));
  const guide = document.querySelector(".settings-howto a");
  assert.equal(guide.getAttribute("href"), "https://example.com/linear.md#set-it-up");
  assert.match(guide.textContent, /setup guide/);

  // Linear on, with no teams mapped, says so rather than showing an empty list.
  linear = true;
  await go("#/settings/github");
  await eventually(() => assert.equal(heading(), "GitHub"));
  await go("#/settings/linear");
  await eventually(() => assert.match(document.querySelector(".settings-facts")?.textContent || "", /app-1/));
  assert.equal(document.querySelector('[aria-labelledby="teams-title"] .list'), null);
  assert.match(document.querySelector('[aria-labelledby="teams-title"] .empty').textContent, /No teams are mapped/);

  // Presets and agents: horizontal pipelines, extends/override badges, click-to-inspect scripts & prompts, grouped agents, and read-only.
  await go("#/settings/presets");
  await eventually(() => assert.equal(heading(), "Presets and agents"));
  assert.equal(marked(), "Presets and agents");
  await eventually(() => assert.deepEqual([...document.querySelectorAll('[aria-label="Steps of default"] .step-name')].map(name => name.textContent), ["checkout", "implement", "review"]));
  assert.equal(document.querySelectorAll(".preset-pipeline").length, 2, "each preset renders a horizontal pipeline");
  const neonCard = [...document.querySelectorAll(".preset-card")].find(card => card.textContent.includes("neon"));
  assert.match(neonCard.textContent, /extends default/);
  assert.match(neonCard.textContent, /Antigravity.*gemini-4-neon-high/);
  assert.match(neonCard.querySelector('[aria-label="Steps of neon"]').textContent, /Shell.*checkout\.sh/);
  assert.match(neonCard.querySelector('[aria-label="Steps of neon"]').textContent, /Agent.*overridden/);

  // Clicking a shell step opens its command and script; clicking again closes it.
  await act(async () => neonCard.querySelector('button[aria-label*="checkout"]').click());
  assert.match(neonCard.querySelector(".step-inspector").textContent, /factory\/scripts\/checkout\.sh/);
  assert.match(neonCard.querySelector(".step-inspector").textContent, /git clone repo/);
  await act(async () => neonCard.querySelector('button[aria-label="Close step details"]').click());
  assert.equal(neonCard.querySelector(".step-inspector"), null);

  // Clicking an overridden script+agent step shows script, effective model, override indicator, front matter, and prompt.
  await act(async () => neonCard.querySelector('button[aria-label*="review"]').click());
  const reviewInspector = neonCard.querySelector(".step-inspector").textContent;
  assert.match(reviewInspector, /factory\/scripts\/review\.sh/);
  assert.match(reviewInspector, /factory\/agents\/review\.md/);
  assert.match(reviewInspector, /Overridden/);
  assert.match(reviewInspector, /harness: claude/);
  assert.match(reviewInspector, /Review the diff carefully\./);

  // Agents list groups reusable roles, collapses duplicate prompts into one row, and collapses preset-specific & unused agents behind toggles.
  const reusableRows = [...document.querySelectorAll('[aria-label="Reusable agents"] .agent-row')];
  assert.equal(reusableRows.length, 2);
  assert.match(reusableRows[0].textContent, /implement.*2 variants · same prompt.*default, legacy, neon/);
  await act(async () => reusableRows[0].querySelector("button.agent-row-toggle").click());
  const groupedInspector = reusableRows[0].querySelector(".agent-inspector").textContent;
  assert.match(groupedInspector, /Variants sharing this prompt/);
  assert.match(groupedInspector, /factory\/agents\/implement\.md/);
  assert.match(groupedInspector, /factory\/agents\/implement-legacy\.md/);
  assert.match(groupedInspector, /neon.*implement.*gemini-4-neon-high.*Overridden/);
  assert.match(groupedInspector, /Implement the requested change with tests\./);

  // Expanding a single reusable agent shows its front matter, uses with override status, and prompt.
  await act(async () => reusableRows[1].querySelector("button.agent-row-toggle").click());
  const singleInspector = reusableRows[1].querySelector(".agent-inspector").textContent;
  assert.match(singleInspector, /factory\/agents\/review\.md/);
  assert.match(singleInspector, /harness: claude/);
  assert.match(singleInspector, /model: sonnet/);
  assert.match(singleInspector, /neon.*review.*opus.*Overridden/);
  assert.match(singleInspector, /Review the diff carefully\./);

  // Preset-specific agents are collapsed behind a toggle when reusable roles exist.
  assert.equal(document.querySelector('[aria-label="Agents only in spec"]'), null);
  const presetSpecificToggle = [...document.querySelectorAll("button")].find(button => /Show preset-specific \(1\)/.test(button.textContent));
  assert.ok(presetSpecificToggle);
  await act(async () => presetSpecificToggle.click());
  assert.match(document.querySelector('[aria-label="Agents only in spec"]').textContent, /spec-writer.*Antigravity default.*spec/);

  assert.equal(document.querySelector('[aria-label="Unused agents"]'), null, "unused agents are collapsed by default");
  const unusedToggle = [...document.querySelectorAll("button")].find(button => /Show unused \(1\)/.test(button.textContent));
  assert.ok(unusedToggle);
  await act(async () => unusedToggle.click());
  assert.match(document.querySelector('[aria-label="Unused agents"]').textContent, /unused-codex.*Codex default.*No preset/);
  assert.equal(document.querySelector(".settings-body form, .settings-body input, .settings-body textarea"), null, "nothing here edits the factory");
  assert.equal(document.querySelector(".factory-warning"), null, "no factory warning when up to date");

  // Outdated factory files warn on Settings overview and on Presets and agents.
  factoryUpdates = [
    { path: "agents/publish.md", status: "edited" },
    { path: "scripts/publish.sh", status: "outdated" },
    { path: "scripts/revise.sh", status: "missing" },
  ];
  await go("#/settings/overview");
  await eventually(() => assert.equal(heading(), "Overview"));
  await eventually(() => assert.match(document.querySelector(".setup-checks")?.textContent || "", /Out of dateFactory/));
  assert.match(document.querySelector(".setup-checks").textContent, /machinist factory upgrade/);
  assert.ok(document.querySelector('a[aria-label="Open Presets and agents"][href="#/settings/presets"]'));
  assert.ok(document.querySelector('[aria-label="Factory files are out of date"]'));
  await go("#/settings/presets");
  await eventually(() => assert.equal(heading(), "Presets and agents"));
  await eventually(() => assert.ok(document.querySelector(".factory-warning")));
  const warningText = document.querySelector(".factory-warning").textContent;
  assert.match(warningText, /3 factory files are out of date/);
  assert.match(warningText, /machinist factory upgrade/);
  assert.match(warningText, /agents\/publish\.md \(edited locally\)/);
  assert.match(warningText, /scripts\/publish\.sh/);
  assert.match(warningText, /scripts\/revise\.sh \(missing\)/);
  factoryUpdates = [];

  // Tokens: revoked tokens are hidden by default, prefixes and "No prefix" show on rows, and tokens can be created, regenerated and revoked.
  await go("#/settings/tokens");
  await eventually(() => assert.equal(heading(), "Tokens"));
  assert.equal(marked(), "Tokens");
  await eventually(() => assert.equal(document.querySelectorAll('[aria-label="Tokens"] .token-row').length, 2));
  assert.equal(document.querySelectorAll('.token-row[data-revoked]').length, 0, "revoked tokens are hidden by default");
  assert.match(document.querySelector(".page-sub").textContent, /2 active tokens, 1 revoked/);
  const initialRows = [...document.querySelectorAll('[aria-label="Tokens"] .token-row')];
  assert.match(initialRows[0].textContent, /alice.*You.*Person.*mch_1a2b…/);
  assert.match(initialRows[1].textContent, /vm-runner.*Runner.*No prefix/);

  // Expanding the collapsed revoked toggle reveals revoked tokens, and collapsing hides them again.
  const revokedToggle = () => [...document.querySelectorAll("button")].find(button => /revoked \(\d+\)/.test(button.textContent));
  assert.match(revokedToggle().textContent, /Show revoked \(1\)/);
  await act(async () => revokedToggle().click());
  assert.equal(document.querySelectorAll('[aria-label="Revoked tokens"] .token-row[data-revoked]').length, 1);
  assert.equal(document.querySelector('[aria-label="Revoke old"]'), null, "a revoked token cannot be revoked again");
  assert.match(revokedToggle().textContent, /Hide revoked \(1\)/);
  await act(async () => revokedToggle().click());
  assert.equal(document.querySelector('[aria-label="Revoked tokens"]'), null);

  // Creating a new token shows the full value once with a copy button and adds its row with a prefix.
  await act(async () => [...document.querySelectorAll("button")].find(button => button.textContent.includes("New token")).click());
  await type("#token-name", "mac-mini");
  await act(async () => {
    const select = document.querySelector("#token-role");
    select.value = "runner";
    select.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
  await act(async () => document.querySelector('.token-form button[type="submit"]').click());
  await eventually(() => assert.ok(document.querySelector('[aria-label="New token value"]')));
  assert.deepEqual(created, [{ name: "mac-mini", role: "runner" }]);
  const reveal = document.querySelector('[aria-label="New token value"]');
  assert.match(reveal.textContent, /won't be shown again/i);
  assert.equal(reveal.querySelector(".token-secret").textContent, `mch_abcd${"0".repeat(60)}`);
  await act(async () => [...reveal.querySelectorAll("button")].find(button => button.textContent.includes("Copy")).click());
  assert.equal(copiedText, `mch_abcd${"0".repeat(60)}`);
  assert.match(reveal.textContent, /Copied/);
  assert.equal(document.querySelectorAll('[aria-label="Tokens"] .token-row').length, 3);
  assert.match(document.querySelectorAll('[aria-label="Tokens"] .token-row')[2].textContent, /mac-mini.*Runner.*mch_abcd…/);
  await act(async () => document.querySelector('[aria-label="Dismiss token"]').click());
  assert.equal(document.querySelector('[aria-label="New token value"]'), null);

  // Regenerating a legacy token asks first, reveals the new token once, updates its prefix, and keeps revoked rows hidden.
  await act(async () => document.querySelector('[aria-label="Regenerate vm-runner"]').click());
  assert.deepEqual(regenerated, [], "regenerating asks first");
  assert.match(document.querySelector('[aria-label="Regenerate vm-runner?"]').textContent, /update the runner/);
  await act(async () => [...document.querySelectorAll('[aria-label="Regenerate vm-runner?"] button')].find(button => button.textContent === "Regenerate").click());
  await eventually(() => assert.equal(document.querySelector(".token-secret")?.textContent, `mch_ef99${"1".repeat(60)}`));
  assert.deepEqual(regenerated, ["vm-runner"]);
  const vmRow = [...document.querySelectorAll('[aria-label="Tokens"] .token-row')].find(row => row.textContent.includes("vm-runner"));
  assert.match(vmRow.textContent, /mch_ef99…/);
  assert.doesNotMatch(vmRow.textContent, /No prefix/);
  assert.match(revokedToggle().textContent, /Show revoked \(2\)/);

  // Navigating away and back never shows the plaintext token again.
  await go("#/settings/github");
  await eventually(() => assert.equal(heading(), "GitHub"));
  await go("#/settings/tokens");
  await eventually(() => assert.equal(heading(), "Tokens"));
  await eventually(() => assert.equal(document.querySelectorAll('[aria-label="Tokens"] .token-row').length, 3));
  assert.equal(document.querySelector('[aria-label="New token value"]'), null);

  // Revoking asks first, hides the revoked token from the active list, and revoking your own signs you out.
  await act(async () => document.querySelector('[aria-label="Revoke vm-runner"]').click());
  assert.deepEqual(revoked, [], "revoking asks first");
  assert.match(document.querySelector('[aria-label="Revoke vm-runner?"]').textContent, /Its runner stops/);
  await act(async () => [...document.querySelectorAll('[aria-label="Revoke vm-runner?"] button')].find(button => button.textContent === "Revoke").click());
  await eventually(() => assert.equal(document.querySelectorAll('[aria-label="Tokens"] .token-row').length, 2));
  assert.match(revokedToggle().textContent, /Show revoked \(3\)/);
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
