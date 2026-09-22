import assert from "node:assert/strict";
import test from "node:test";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const jobs = [
  { id: "job_failed", state: "failed", prompt: "Failed fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_failed", state: "failed", command: "codex" }] },
  { id: "job_succeeded", state: "succeeded", prompt: "Succeeded fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_succeeded", state: "succeeded", command: "codex" }] },
];

test("runs default to board view and share filters when switching views", async (context) => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/runs" });
  const priorGlobals = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent"]) {
    priorGlobals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  priorGlobals.set("fetch", Object.getOwnPropertyDescriptor(globalThis, "fetch"));
  let artifactRequests = 0;
  const detailJob = {
    id: "job_detail", state: "succeeded", repository: "example/repo", command: "build",
    task: { title: "Task with files", spec: "Build the feature" },
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:01:00Z",
    runs: [
      { id: "plan", command: "plan", state: "succeeded", outcome: "complete", summary: "Planned" },
      { id: "build", command: "build", state: "succeeded", outcome: "complete", summary: "Built", executor: "test-executor", model: "test-model", worker_name: "test-worker", duration_millis: 1000, exit_code: 0 },
    ],
  };
  globalThis.fetch = async url => {
    if (url.endsWith("/artifacts")) {
      artifactRequests += 1;
      return { ok: true, json: async () => [
        { id: "file_plan", run_id: "plan", path: "plan.md", size: 10, content_type: "text/plain" },
        { id: "file_build", run_id: "build", path: "result.md", size: 10, content_type: "text/plain" },
      ] };
    }
    if (url.endsWith("/content")) return { ok: true, text: async () => "<script>literal file text</script>" };
    return { ok: true, json: async () => ({ jobs: [...jobs, detailJob], workers: [], commands: [], repositories: [], triggers: [], csrf_token: "test" }) };
  };

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

  assert.equal(button("Board").getAttribute("aria-pressed"), "true");
  assert.ok(document.querySelector('a[href="#/runs/job_failed"]'), "board links to task");
  assert.equal(button("List").getAttribute("aria-pressed"), "false");

  button("Board").click();
  await eventually(() => assert.equal(button("Board").getAttribute("aria-pressed"), "true"));
  assert.match(document.body.textContent, /Waiting to start/);

  button("Failed").click();
  await eventually(() => assert.doesNotMatch(document.body.textContent, /Succeeded fixture/));
  assert.match(document.body.textContent, /Failed fixture/);

  button("List").click();
  await eventually(() => assert.equal(button("List").getAttribute("aria-pressed"), "true"));
  assert.equal(button("Failed").getAttribute("aria-pressed"), "true");
  assert.ok(document.querySelector('a[href="#/runs/job_failed"]'), "list links to task");
  assert.match(document.body.textContent, /Failed fixture/);
  assert.doesNotMatch(document.body.textContent, /Succeeded fixture/);

  window.location.hash = "#/runs/job_detail";
  await eventually(() => assert.match(document.body.textContent, /Task with files/));
  await eventually(() => assert.ok(document.querySelector('[aria-label="View result.md"]')));
  assert.equal(artifactRequests, 1, "metadata fetched once across all panels and attempts");
  const tab = name => [...document.querySelectorAll('[role="tab"]')].find(el => el.textContent === name);
  tab("Details").click();
  await eventually(() => assert.equal(tab("Details").getAttribute("aria-selected"), "true"));
  const details = document.querySelector('[role="tabpanel"]:not([hidden])');
  for (const text of ["test-executor", "test-model", "test-worker", "Not reported", "Delete task", "Exit code"]) {
    assert.ok(details.textContent.includes(text), `details include ${text}`);
  }
  tab("History").click();
  await eventually(() => assert.equal(tab("History").getAttribute("aria-selected"), "true"));
  assert.match(document.querySelector('[role="tabpanel"]:not([hidden])').textContent, /plan.md/);
  tab("Files").click();
  await eventually(() => assert.equal(tab("Files").getAttribute("aria-selected"), "true"));
  document.querySelector('[role="tabpanel"]:not([hidden]) [aria-label="View result.md"]').click();
  await eventually(() => assert.match(document.querySelector('[role="tabpanel"]:not([hidden])').textContent, /<script>literal file text<\/script>/));
  assert.equal(document.querySelectorAll("script").length, 0, "preview renders text, not markup");
  assert.equal(artifactRequests, 1, "changing tabs does not refetch metadata");
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
