import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
import { canStart, harnessOptionLabel, initialInputValues, initialPreset, keyFor, modelPlaceholder, newJobKey, presetInputs, runRequest, supportedHarnesses, titleOf } from "./new-job.js";

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

test("presetInputs, initialInputValues, canStart and runRequest handle declared preset inputs", () => {
  const deploy = {
    name: "deploy",
    inputs: {
      region: { description: "AWS region", default: "us-east-1" },
      environment: { description: "Target environment" },
      notes: { description: "Optional notes", default: "" },
    },
  };
  const declared = presetInputs(deploy);
  assert.deepEqual(declared, [
    { name: "environment", description: "Target environment", default: undefined, required: true },
    { name: "notes", description: "Optional notes", default: "", required: false },
    { name: "region", description: "AWS region", default: "us-east-1", required: false },
  ]);
  assert.deepEqual(initialInputValues(deploy), { environment: "", notes: "", region: "us-east-1" });
  assert.equal(canStart({ preset: "deploy", repo: "owner/repo", prompt: "Deploy", inputs: { environment: "", region: "us-east-1" }, presetInputs: declared }), false);
  assert.equal(canStart({ preset: "deploy", repo: "owner/repo", prompt: "Deploy", inputs: { environment: "staging", region: "us-east-1" }, presetInputs: declared }), true);
  assert.deepEqual(
    runRequest({ key: "ui-1", preset: "deploy", repo: "owner/repo", prompt: "Deploy", inputs: { environment: "staging", notes: "", region: "eu-west-1" }, presetInputs: declared }),
    { key: "ui-1", preset: "deploy", vars: { repo: "owner/repo", environment: "staging", notes: "", region: "eu-west-1" }, prompt: "Deploy" },
  );
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

test("NewJobPage renders declared preset inputs, enforces required fields, and posts them in vars", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM();
  const posted = [];
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/v1/presets") {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          default: "default",
          presets: [
            { name: "default", description: "Default.", steps: [{ name: "work", agents: [] }] },
            {
              name: "deploy",
              description: "Deploy service.",
              inputs: {
                environment: { description: "Target env" },
                region: { description: "AWS region", default: "us-east-1" },
              },
              steps: [{ name: "deploy", agents: [] }],
            },
          ],
        }),
      };
    }
    if (url === "/api/v1/repositories") {
      return { ok: true, status: 200, json: async () => ({ repositories: ["owner/repo"] }) };
    }
    if (url === "/api/v1/runners") {
      return { ok: true, status: 200, json: async () => ({ runners: [] }) };
    }
    if (url === "/machinist/run" && options.method === "POST") {
      posted.push(JSON.parse(options.body));
      return { ok: true, status: 201, json: async () => ({ id: "t_deploy1" }) };
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
  await act(async () => root.render(React.createElement(NewJobPage, { onSignedOut() {} })));
  await eventually(() => assert.ok(document.querySelector("#new-job-preset")));

  // Default preset declares no inputs.
  assert.equal(document.querySelector(".preset-inputs"), null);

  // Switch to deploy preset: inputs render with default pre-filled and required marked.
  await choose("#new-job-preset", "deploy");
  assert.ok(document.querySelector(".preset-inputs"));
  assert.equal(document.querySelector("#new-job-input-region").value, "us-east-1");
  assert.equal(document.querySelector("#new-job-input-environment").required, true);
  assert.equal(document.querySelector("#new-job-input-region").required, false);

  await type("#new-job-prompt", "Deploy release");
  const submitBtn = () => document.querySelector('form button[type="submit"]');
  assert.equal(submitBtn().disabled, true, "required input environment is still empty");

  await type("#new-job-input-environment", "staging");
  assert.equal(submitBtn().disabled, false);

  await act(async () => submitBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(posted.length, 1));
  assert.deepEqual(posted[0].vars, { repo: "owner/repo", environment: "staging", region: "us-east-1" });
});

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
