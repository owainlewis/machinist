import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const jobs = [
  { id: "job_failed", state: "failed", prompt: "Failed fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_failed", state: "failed", command: "codex" }] },
  { id: "job_running", state: "running", prompt: "Running fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_running", state: "running", command: "codex", worker_name: "laptop", started_at: "2026-01-01T00:00:00Z" }] },
  { id: "job_succeeded", state: "succeeded", prompt: "Succeeded fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_succeeded", state: "succeeded", command: "codex" }] },
];

test("home, task list, board, search and task detail work against live status", async (context) => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/home", pretendToBeVisual: true });
  const priorGlobals = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "KeyboardEvent", "FocusEvent", "PointerEvent", "Node", "Element", "HTMLElement", "HTMLInputElement", "HTMLButtonElement", "ShadowRoot", "DocumentFragment", "MutationObserver", "getComputedStyle", "requestAnimationFrame", "cancelAnimationFrame"]) {
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
      { id: "build", command: "build", state: "succeeded", outcome: "complete", summary: "**Built** the `feature`\n\n- tests pass\n\n<script>window.injected = true</script>", executor: "test-executor", model: "test-model", worker_name: "test-worker", duration_millis: 1000, exit_code: 0 },
    ],
  };
  const interruptedJob = { ...detailJob, id: "job_interrupted", state: "interrupted", workflow: { name: "build", steps: ["build"], current_step: 0 }, runs: [{ id: "interrupted", command: "build", state: "interrupted" }] };
  let cancelled = false;
  globalThis.fetch = async (url, options) => {
    if (url.endsWith("/cancel") && options?.method === "POST") {
      cancelled = true;
      interruptedJob.state = "cancelled";
      return { ok: true, json: async () => ({}) };
    }
    if (url.endsWith("/artifacts")) {
      artifactRequests += 1;
      return { ok: true, json: async () => [
        { id: "file_plan", run_id: "plan", path: "plan.md", size: 10, content_type: "text/plain" },
        { id: "file_build", run_id: "build", path: "result.md", size: 10, content_type: "text/plain" },
      ] };
    }
    if (url.endsWith("/content")) return { ok: true, text: async () => "<script>literal file text</script>" };
    return { ok: true, json: async () => ({ jobs: [...jobs, detailJob, interruptedJob], workers: [], commands: [], repositories: [], triggers: [], csrf_token: "test" }) };
  };

  const server = await createServer({ server: { middlewareMode: true }, appType: "custom" });
  let mountedRoot;
  context.after(async () => {
    const previousActEnvironment = globalThis.IS_REACT_ACT_ENVIRONMENT;
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    await act(async () => mountedRoot?.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment;
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of priorGlobals) {
      if (descriptor === undefined) delete globalThis[name];
      else Object.defineProperty(globalThis, name, descriptor);
    }
  });

  mountedRoot = (await server.ssrLoadModule("/src/main.jsx")).appRoot;

  // Home lists what needs you, with a spinner on running work.
  await eventually(() => assert.match(document.body.textContent, /Failed fixture/));
  const needsYou = document.querySelector('section[aria-label="Needs you"]');
  assert.match(needsYou.textContent, /Failed fixture/);
  assert.doesNotMatch(needsYou.textContent, /Succeeded fixture/);
  assert.ok(document.querySelector('section[aria-label="In progress"] .spinner'), "running tasks spin");
  assert.ok(document.querySelector('form[aria-label="New task"] textarea'), "home has the task composer");

  assert.ok(document.querySelector('form[aria-label="New task"] [aria-label="Repository"]'), "composer uses the shared select");

  window.location.hash = "#/settings";
  await eventually(() => assert.ok(button("Add repository")));
  button("Add repository").click();
  const dialog = await eventually(() => { const found = document.querySelector('[role="dialog"]'); assert.ok(found, "repository dialog opens"); return found; });
  assert.match(dialog.textContent, /\[repositories\.my-project\]/, "dialog shows the worker.toml block to paste");
  assert.doesNotMatch(document.querySelector("main").textContent, /Average task time/, "settings no longer shows usage metrics");
  assert.ok(dialog.querySelector('[aria-label="Close"]'), "dialog can be closed");

  // Leaving the page unmounts the dialog (jsdom never finishes its exit transition).
  window.location.hash = "#/usage";
  await eventually(() => assert.match(document.querySelector("main").textContent, /Average task time/));
  await eventually(() => assert.equal(document.querySelector('[role="dialog"]'), null));

  window.location.hash = "#/tasks";
  await eventually(() => assert.equal(button("List").getAttribute("aria-pressed"), "true"));
  assert.ok(document.querySelector('a[href="#/tasks/job_failed"]'), "list links to task");
  for (const group of ["Failed", "Running", "Done"]) assert.ok(document.querySelector(`section[aria-label="${group}"]`), `${group} group shown`);

  button("Board").click();
  await eventually(() => assert.equal(button("Board").getAttribute("aria-pressed"), "true"));
  assert.ok(document.querySelector("#board-needs"), "board has a Needs you column");
  assert.match(document.querySelector('section[aria-labelledby="board-needs"]').textContent, /Failed fixture/);

  const search = document.querySelector('input[aria-label="Search tasks"]');
  setInput(search, "succeeded");
  await eventually(() => assert.doesNotMatch(document.body.textContent, /Failed fixture/));
  assert.match(document.body.textContent, /Succeeded fixture/);

  button("List").click();
  await eventually(() => assert.equal(button("List").getAttribute("aria-pressed"), "true"));
  assert.match(document.body.textContent, /Succeeded fixture/);
  assert.doesNotMatch(document.body.textContent, /Failed fixture/);

  window.location.hash = "#/tasks/job_detail";
  await eventually(() => assert.match(document.body.textContent, /Task with files/));
  await eventually(() => assert.ok(document.querySelector('[aria-label="View result.md"]')));
  const summary = document.querySelector('section[aria-label="Current result"] .markdown');
  assert.equal(summary.querySelector("strong")?.textContent, "Built", "summary markdown is rendered");
  assert.equal(summary.querySelector("code")?.textContent, "feature");
  assert.equal(summary.querySelector("li")?.textContent, "tests pass");
  assert.match(summary.textContent, /<script>window\.injected = true<\/script>/, "raw HTML stays visible as text");
  assert.equal(document.querySelectorAll("script").length, 0, "agent HTML is never rendered as markup");
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
  window.location.hash = "#/runs/job_interrupted";
  await eventually(() => assert.ok([...document.querySelectorAll("button")].find(el => el.textContent === "Cancel task")));
  button("Cancel task").click();
  await eventually(() => assert.equal(cancelled, true));
  await eventually(() => assert.equal(button("Delete task").disabled, false));
  await eventually(() => assert.equal([...document.querySelectorAll("button")].find(el => el.textContent === "Cancel task"), undefined));

});

function setInput(input, value) {
  Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set.call(input, value);
  input.dispatchEvent(new window.Event("input", { bubbles: true }));
}

function button(label) {
  const match = [...document.querySelectorAll("button")].find((element) => element.textContent.includes(label));
  assert.ok(match, `button ${label} should exist`);
  return match;
}

async function eventually(assertion) {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try { return assertion(); } catch (error) {
      if (attempt === 49) throw error;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
}
