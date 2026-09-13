import assert from "node:assert/strict";
import test from "node:test";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const jobs = [
  { id: "job_failed", state: "failed", prompt: "Failed fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_failed", state: "failed", command: "codex" }] },
  { id: "job_succeeded", state: "succeeded", prompt: "Succeeded fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_succeeded", state: "succeeded", command: "codex" }] },
];

test("runs default to table view and share filters when switching views", async (context) => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/runs" });
  const priorGlobals = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent"]) {
    priorGlobals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  priorGlobals.set("fetch", Object.getOwnPropertyDescriptor(globalThis, "fetch"));
  globalThis.fetch = async () => ({ ok: true, json: async () => ({ jobs, workers: [], commands: [], repositories: [], triggers: [], csrf_token: "test" }) });

  const server = await createServer({ server: { middlewareMode: true }, appType: "custom" });
  context.after(async () => {
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of priorGlobals) {
      if (descriptor === undefined) delete globalThis[name];
      else Object.defineProperty(globalThis, name, descriptor);
    }
  });

  await server.ssrLoadModule("/src/main.jsx");
  await eventually(() => assert.match(document.body.textContent, /Failed fixture/));

  assert.match(document.body.textContent, /Submitted/);
  assert.equal(button("Table").getAttribute("aria-pressed"), "true");

  button("Board").click();
  await eventually(() => assert.equal(button("Board").getAttribute("aria-pressed"), "true"));
  assert.match(document.body.textContent, /Waiting to start/);

  button("Failed").click();
  await eventually(() => assert.doesNotMatch(document.body.textContent, /Succeeded fixture/));
  assert.match(document.body.textContent, /Failed fixture/);

  button("Table").click();
  await eventually(() => assert.match(document.body.textContent, /Submitted/));
  assert.equal(button("Failed").getAttribute("aria-pressed"), "true");
  assert.match(document.body.textContent, /Failed fixture/);
  assert.doesNotMatch(document.body.textContent, /Succeeded fixture/);
});

function button(label) {
  const match = [...document.querySelectorAll("button")].find((element) => element.textContent.includes(label));
  assert.ok(match, `button ${label} should exist`);
  return match;
}

async function eventually(assertion) {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try { assertion(); return; } catch (error) {
      if (attempt === 49) throw error;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
}
