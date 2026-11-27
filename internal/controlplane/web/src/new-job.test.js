import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
import { canStart, initialInputValues, initialPreset, keyFor, newJobKey, presetInputs, runRequest, titleOf } from "./new-job.js";

test("a new job is a run API request with a key of its own", () => {
  assert.equal(newJobKey(() => "abc"), "ui-abc");
  assert.notEqual(newJobKey(), newJobKey());
  assert.match(newJobKey(), /^ui-[0-9a-f]{32}$/);
  assert.deepEqual(runRequest({ key: "ui-1", preset: "coding", repo: "owner/repo", prompt: "  Fix it \n" }), { key: "ui-1", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it" });
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
  assert.equal(keyFor(first, { repo: "owner/repo", prompt: "Fix it better" }, make).key, "ui-2");
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

test("NewJobPage renders declared preset inputs, enforces required fields, and posts them in vars", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/new" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "Event", "MouseEvent", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
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
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  const { NewJobPage } = await server.ssrLoadModule("/src/new-job-page.jsx");
  await act(async () => root.render(React.createElement(NewJobPage, { onSignedOut() {} })));
  for (let i = 0; i < 40 && !document.querySelector("#new-job-preset"); i++) {
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }

  // Default preset declares no inputs.
  assert.equal(document.querySelector(".preset-inputs"), null);

  // Switch to deploy preset: inputs render with default pre-filled and required marked.
  const presetSelect = document.querySelector("#new-job-preset");
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(presetSelect), "value").set.call(presetSelect, "deploy");
    presetSelect.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
  assert.ok(document.querySelector(".preset-inputs"));
  assert.equal(document.querySelector("#new-job-input-region").value, "us-east-1");
  assert.equal(document.querySelector("#new-job-input-environment").required, true);
  assert.equal(document.querySelector("#new-job-input-region").required, false);

  const setVal = async (selector, value) => {
    const el = document.querySelector(selector);
    await act(async () => {
      Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value").set.call(el, value);
      el.dispatchEvent(new window.Event("input", { bubbles: true }));
    });
  };
  await setVal("#new-job-prompt", "Deploy release");
  const submitBtn = () => document.querySelector('form button[type="submit"]');
  assert.equal(submitBtn().disabled, true, "required input environment is still empty");

  await setVal("#new-job-input-environment", "staging");
  assert.equal(submitBtn().disabled, false);

  await act(async () => submitBtn().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  assert.equal(posted.length, 1);
  assert.deepEqual(posted[0].vars, { repo: "owner/repo", environment: "staging", region: "us-east-1" });
});
