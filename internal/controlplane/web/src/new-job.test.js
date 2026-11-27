import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
import { canStart, harnessOptionLabel, initialPreset, keyFor, modelPlaceholder, newJobKey, runRequest, supportedHarnesses, titleOf } from "./new-job.js";

test("a new job is a run API request with a key of its own", () => {
  assert.equal(newJobKey(() => "abc"), "ui-abc");
  assert.notEqual(newJobKey(), newJobKey());
  assert.match(newJobKey(), /^ui-[0-9a-f]{32}$/);
  assert.deepEqual(
    runRequest({ key: "ui-1", preset: "coding", repo: "owner/repo", prompt: "  Fix it \n" }),
    { key: "ui-1", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it" },
  );
  assert.deepEqual(
    runRequest({ key: "ui-2", preset: "coding", repo: "owner/repo", prompt: "Fix it", harness: "  ", model: "  " }),
    { key: "ui-2", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it" },
  );
  assert.deepEqual(
    runRequest({ key: "ui-3", preset: "coding", repo: "owner/repo", prompt: "Fix it", harness: " codex ", model: " gpt-5-codex " }),
    { key: "ui-3", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it", harness: "codex", model: "gpt-5-codex" },
  );
  assert.deepEqual(
    runRequest({ key: "ui-4", preset: "coding", repo: "owner/repo", prompt: "Fix it", harness: "claude", model: "" }),
    { key: "ui-4", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it", harness: "claude" },
  );
  assert.deepEqual(
    runRequest({ key: "ui-5", preset: "coding", repo: "owner/repo", prompt: "Fix it", harness: "", model: "opus" }),
    { key: "ui-5", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it", model: "opus" },
  );
});

test("a job needs a preset, a repository and a prompt", () => {
  assert.equal(canStart({ preset: "coding", repo: "owner/repo", prompt: "Fix it" }), true);
  for (const missing of [{ preset: "" }, { repo: "" }, { prompt: "   \n" }]) {
    assert.equal(canStart({ preset: "coding", repo: "owner/repo", prompt: "Fix it", ...missing }), false);
  }
});

test("the default preset is chosen first when it exists", () => {
  const presets = [{ name: "coding" }, { name: "spec" }];
  assert.equal(initialPreset(presets, "spec"), "spec");
  assert.equal(initialPreset(presets, "ghost"), "coding");
  assert.equal(initialPreset([], "coding"), "");
});

test("the title is the prompt's first line, cut like the server cuts it", () => {
  assert.equal(titleOf("\n  Fix the toasts  \nMore detail"), "Fix the toasts");
  assert.equal(titleOf("x".repeat(80)), "x".repeat(80));
  assert.equal(titleOf("é".repeat(90)), `${"é".repeat(79)}…`);
  assert.equal(titleOf(""), "");
});

test("a retry of the same fields keeps its key and an edit gets a new one", () => {
  let count = 0;
  const make = () => `ui-${++count}`;
  const first = keyFor(null, { repo: "owner/repo", prompt: "Fix it" }, make);
  assert.equal(first.key, "ui-1");
  assert.equal(keyFor(first, { repo: "owner/repo", prompt: "Fix it" }, make), first);
  assert.equal(keyFor(first, { repo: "owner/repo", prompt: "Fix it", harness: "", model: "  " }, make), first);
  assert.equal(keyFor(first, { repo: "owner/repo", prompt: "Fix it better" }, make).key, "ui-2");
  const withHarness = keyFor(first, { repo: "owner/repo", prompt: "Fix it", harness: "codex" }, make);
  assert.equal(withHarness.key, "ui-3");
  assert.equal(keyFor(withHarness, { repo: "owner/repo", prompt: "Fix it", harness: " codex ", model: "" }, make), withHarness);
  const withModel = keyFor(withHarness, { repo: "owner/repo", prompt: "Fix it", harness: "codex", model: "gpt-5-codex" }, make);
  assert.equal(withModel.key, "ui-4");
});

test("harness labels show online status and model placeholders match the selected harness", () => {
  assert.deepEqual(supportedHarnesses, ["agy", "claude", "codex", "pi"]);
  assert.equal(harnessOptionLabel("claude", new Set()), "Claude Code");
  assert.equal(harnessOptionLabel("claude", new Set(["claude"])), "Claude Code");
  assert.equal(harnessOptionLabel("codex", new Set(["claude"])), "Codex (no online runner)");

  assert.equal(modelPlaceholder(""), "Preset / harness default");
  assert.match(modelPlaceholder("claude"), /sonnet|opus/);
  assert.match(modelPlaceholder("codex"), /gpt-5-codex/);
  assert.match(modelPlaceholder("agy"), /gemini/);
  assert.match(modelPlaceholder("pi"), /Harness default/);
});

function setupDOM() {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/new" });
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

test("NewJobPage lets you pick an optional harness and model and clears model when switching harness", { timeout: 30000 }, async context => {
  const { cleanup } = setupDOM();
  const submitted = [];
  const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/v1/presets") return reply(200, { default: "coding", presets: [{ name: "coding", description: "Implement.", steps: [] }] });
    if (url === "/api/v1/repositories") return reply(200, { repositories: ["owner/repo"] });
    if (url === "/api/v1/runners") return reply(200, { runners: [{ name: "r1", harnesses: ["steps", "claude"] }], queue: { waiting: 0 } });
    if (url === "/machinist/run" && options.method === "POST") {
      submitted.push(JSON.parse(options.body));
      return reply(201, { id: "t_1", state: "queued" });
    }
    throw new Error(`unexpected ${url}`);
  };
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });
  const { NewJobPage } = await server.ssrLoadModule("/src/new-job-page.jsx");
  await act(async () => root.render(React.createElement(NewJobPage, { onSignedOut: () => {} })));

  await eventually(() => assert.ok(document.querySelector("#new-job-harness")));
  const harnessOptions = [...document.querySelectorAll("#new-job-harness option")].map(opt => opt.textContent);
  assert.deepEqual(harnessOptions, [
    "Preset default",
    "Antigravity (no online runner)",
    "Claude Code",
    "Codex (no online runner)",
    "Pi (no online runner)",
  ]);

  const modelInput = () => document.querySelector("#new-job-model");
  assert.equal(modelInput().placeholder, "Preset / harness default");

  await choose("#new-job-harness", "claude");
  assert.match(modelInput().placeholder, /sonnet|opus/);
  await type("#new-job-model", "opus");
  assert.equal(modelInput().value, "opus");

  // Switching harness clears model and updates placeholder.
  await choose("#new-job-harness", "codex");
  assert.equal(modelInput().value, "");
  assert.match(modelInput().placeholder, /gpt-5-codex/);
  await type("#new-job-model", "gpt-5-codex");
  await type("#new-job-prompt", "Add tests for harness selection");

  await act(async () => document.querySelector('form button[type="submit"]').click());
  await eventually(() => assert.equal(submitted.length, 1));
  assert.deepEqual(
    { ...submitted[0], key: undefined },
    { key: undefined, preset: "coding", vars: { repo: "owner/repo" }, prompt: "Add tests for harness selection", harness: "codex", model: "gpt-5-codex" },
  );
});

test("NewJobPage still loads when /api/v1/runners fails", { timeout: 30000 }, async context => {
  const { cleanup } = setupDOM();
  const reply = (status, body) => ({ ok: status < 400, status, json: async () => body });
  globalThis.fetch = async url => {
    if (url === "/api/v1/presets") return reply(200, { default: "coding", presets: [{ name: "coding", description: "Implement.", steps: [] }] });
    if (url === "/api/v1/repositories") return reply(200, { repositories: ["owner/repo"] });
    if (url === "/api/v1/runners") return reply(500, { error: "runners unavailable" });
    throw new Error(`unexpected ${url}`);
  };
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });
  const { NewJobPage } = await server.ssrLoadModule("/src/new-job-page.jsx");
  await act(async () => root.render(React.createElement(NewJobPage, { onSignedOut: () => {} })));

  await eventually(() => assert.ok(document.querySelector("#new-job-harness")));
  const harnessOptions = [...document.querySelectorAll("#new-job-harness option")].map(opt => opt.textContent);
  assert.deepEqual(harnessOptions, ["Preset default", "Antigravity", "Claude Code", "Codex", "Pi"]);
});

async function type(selector, value) {
  const element = document.querySelector(selector);
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "value").set;
  await act(async () => {
    setter.call(element, value);
    element.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}

async function choose(selector, value) {
  const element = document.querySelector(selector);
  await act(async () => {
    element.value = value;
    element.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
}

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}
