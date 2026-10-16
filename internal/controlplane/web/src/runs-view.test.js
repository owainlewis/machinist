import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const jobs = [
  { id: "job_failed", state: "failed", prompt: "Failed fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_failed", state: "failed", command: "codex" }] },
  { id: "job_succeeded", state: "succeeded", prompt: "Succeeded fixture", repository: "example/repo", command: "codex", created_at: "2026-01-01T00:00:00Z", runs: [{ id: "run_succeeded", state: "succeeded", command: "codex" }] },
];

test("compact runs table filters jobs and preserves task actions", async (context) => {
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
  const interruptedJob = { ...detailJob, id: "job_interrupted", state: "interrupted", workflow: { name: "build", steps: ["build"], current_step: 0 }, runs: [{ id: "interrupted", command: "build", state: "interrupted" }] };
  const otherInterruptedJob = { ...interruptedJob, id: "job_interrupted_other", task: { title: "Other interrupted task", spec: "Inspect existing work" }, runs: [{ id: "other-interrupted", command: "build", state: "interrupted" }] };
  const humanDecisionJob = { ...detailJob, id: "job_human_decision", state: "blocked", workflow: { name: "factory", steps: ["triage", "spec", "implement", "review"], current_step: 3, factory: { verdict: "human_decision_needed" } }, runs: [{ id: "human-review", command: "review", state: "succeeded", outcome: "complete", summary: "Please decide how to handle the public API." }] };
  const pendingReviewJob = {...humanDecisionJob,id:"job_pending_review",workflow:{...humanDecisionJob.workflow,factory:{}},runs:[{...humanDecisionJob.runs[0],summary:"CI is pending"}]};
  pendingReviewJob.workflow.factory.pr_url="https://github.com/example/repo/pull/3";
  const githubJob = { ...detailJob, id: "github-issue", state_backend: "github", task: { title: "GitHub journal task", source_url: "https://github.com/example/repo/issues/1", spec: "Build it" }, runs: [{ id: "github-report", command: "triage", outcome: "complete", state: "succeeded", summary: "Researched the request. Read the stage on GitHub.", report: "Full researched handoff stored on GitHub", github_url: "https://github.com/example/repo/issues/1#issuecomment-2" }] };
  let cancelled = false;
  let savedPrompt;
  globalThis.fetch = async (url, options) => {
    if (url.startsWith("/api/v1/definitions")) return { ok: true, json: async () => ({ commands: ["triage", "spec", "implement", "review"].map(name => ({ name, executor: "codex", prompt: `${name} original prompt`, hash: "original-hash", timeout: "20m" })) }) };
    if (url === "/api/v1/factory/agents/triage" && options?.method === "PUT") { savedPrompt = JSON.parse(options.body); return { ok: true, json: async () => ({ hash: "new-hash" }) }; }

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
    return { ok: true, json: async () => ({ state_backend: "local", jobs: [...jobs, detailJob, interruptedJob, otherInterruptedJob, humanDecisionJob, pendingReviewJob, githubJob, { ...otherInterruptedJob, id: "job_interrupted_second", task: { title: "Second interrupted task" }, runs: [{ id: "second-interrupted", command: "build", state: "interrupted" }] }], workers: [], commands: [], workflows: ["factory"], repositories: ["example/repo"], triggers: [], csrf_token: "test" }) };
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
  await eventually(() => assert.match(document.body.textContent, /Failed fixture/));

  assert.equal(document.querySelector(".brand-wordmark").textContent,"Machinist");
  assert.ok(document.querySelector('[role="table"][aria-label="Runs"]'));
  assert.ok(document.querySelector('a[href="#/runs/job_failed"]'), "table links to task");
  assert.match(document.querySelector('[role="table"]').textContent, /Duration/);
  button("Failed").click();
  await eventually(() => assert.doesNotMatch(document.body.textContent, /Succeeded fixture/));
  assert.match(document.body.textContent, /Failed fixture/);

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
  tab("Timeline").click();
  await eventually(() => assert.equal(tab("Timeline").getAttribute("aria-selected"), "true"));
  assert.match(document.querySelector('[role="tabpanel"]:not([hidden])').textContent, /plan.md/);
  tab("Artifacts").click();
  await eventually(() => assert.equal(tab("Artifacts").getAttribute("aria-selected"), "true"));
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

  window.location.hash = "#/inbox";
  await eventually(() => assert.ok(document.querySelector('[role="table"][aria-label="Runs"]')));
  await eventually(() => assert.equal(button("Needs attention").getAttribute("aria-pressed"), "true"));
  assert.match(document.querySelector('[role="table"]').textContent, /Failed fixture/);
  assert.doesNotMatch(document.querySelector('[role="table"]').textContent, /Succeeded fixture/);
  assert.equal(document.querySelector('nav a[href="#/inbox"]'), null);
  // Stop confirmation and feedback still belong only to the selected task attempt.
  window.location.hash = "#/runs/job_interrupted_other";
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Other interrupted task"));
  await eventually(() => assert.ok(document.querySelector('main input[type="checkbox"]')));
  const confirmation = document.querySelector('main input[type="checkbox"]');
  confirmation.click();
  await eventually(() => assert.equal(confirmation.checked, true));
  const feedbackField = document.querySelector("main textarea");
  Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, "value").set.call(feedbackField, "Context only for task A");
  feedbackField.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await eventually(() => assert.equal(feedbackField.value, "Context only for task A"));
  window.location.hash = "#/runs/job_interrupted_second";
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Second interrupted task"));
  await eventually(() => assert.equal(document.querySelector("main textarea").value, ""));
  assert.equal(document.querySelector('main input[type="checkbox"]').checked, false);
  window.location.hash = "#/runs/job_interrupted_other";
  await eventually(() => assert.equal(document.querySelector("h1").textContent, "Other interrupted task"));
  await eventually(() => assert.equal(document.querySelector('main input[type="checkbox"]').checked, false));
  assert.equal(document.querySelector("main textarea").value, "");
  window.location.hash = "#/runs/job_human_decision";
  await eventually(() => assert.ok([...document.querySelectorAll("button")].find(element => element.textContent === "Request changes")));
  assert.ok([...document.querySelectorAll("button")].find(element => element.textContent === "Retry review"));
  assert.equal([...document.querySelectorAll("button")].some(element => element.textContent.includes("Approve and start")), false);
  window.location.hash = "#/runs/job_pending_review";
  await eventually(()=>assert.ok([...document.querySelectorAll("button")].find(el=>el.textContent==="Request changes")));
  assert.ok(button("Retry review"));
  const requestsBeforeGitHubTask = artifactRequests;
  window.location.hash = "#/runs/github-issue";
  await eventually(() => assert.match(document.body.textContent, /Researched the request. Read the stage on GitHub/));
  assert.doesNotMatch(document.body.textContent, /Full researched handoff stored on GitHub/);
  assert.doesNotMatch(document.body.textContent, /triage\.md|spec\.md|implementation\.md/);
  assert.equal(artifactRequests, requestsBeforeGitHubTask, "GitHub reports never load local artifacts");
  assert.ok(document.querySelector('a[href="https://github.com/example/repo/issues/1#issuecomment-2"]'));
  assert.equal([...document.querySelectorAll("button")].some(element => element.textContent === "Delete task"), false);
  assert.equal([...document.querySelectorAll('[role="tab"]')].some(element => ["Artifacts", "Handoffs"].includes(element.textContent)), false);
  assert.ok(document.querySelector('[role="tab"][aria-selected="true"]'), "run has clear Activity and Details tabs");
  assert.equal(document.querySelector("details"),null,"native disclosure toggles are absent");
  window.location.hash = "#/agents";
  await eventually(() => assert.ok(document.querySelector('[aria-label="Factory agents"]')));
  await eventually(() => assert.equal(document.querySelector("textarea").value, "triage original prompt"));
  assert.ok(document.querySelector(".markdown-prompt"), "prompt has a Markdown reading view");
  button("Edit prompt").click();
  assert.equal(button("Save prompt").disabled, true);
  const textarea = document.querySelector("textarea");
  Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, "value").set.call(textarea, "Updated triage prompt");
  textarea.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await eventually(() => assert.equal(button("Save prompt").disabled, false));
  [...document.querySelectorAll(".agent-row")].find(el=>el.textContent.startsWith("Spec")).click();
  await eventually(()=>assert.equal(document.querySelector("textarea").value,"spec original prompt"));
  [...document.querySelectorAll(".agent-row")].find(el=>el.textContent.startsWith("Triage")).click();
  await eventually(()=>assert.equal(document.querySelector("textarea").value,"Updated triage prompt"));
  button("Save prompt").click();
  await eventually(() => assert.deepEqual(savedPrompt, { prompt: "Updated triage prompt", expected_hash: "original-hash", repository: "example/repo" }));
  await eventually(() => assert.match(document.body.textContent, /Saved. Future stage attempts use this Markdown version/));

  window.location.hash = "#/runs";
  await eventually(() => assert.ok([...document.querySelectorAll("button")].find(el => el.textContent === "New run")));
  // Polling adds a project without removing the selected repository or workflow.
  // A retry can then exercise the same submission regardless of machine speed.
  const submissions = [];
  globalThis.fetch = async (url, options) => {
    if (url === "/api/v1/jobs") {
      submissions.push(JSON.parse(options.body));
      if (submissions.length === 1) throw new Error("Response lost after submission");
      return {ok:true,json:async()=>({id:"job_created"})};
    }
    return {ok:true,json:async()=>({jobs,workers:[],commands:[],workflows:["factory"],repositories:["example/repo","repository-alias"],repository_mappings:{"repository-alias":"owner/canonical"},state_backend:"local",csrf_token:"test"})};
  };
  button("New run").click();
  await eventually(()=>assert.ok(document.querySelector("form textarea")));
  const brief = document.querySelector("form textarea");
  Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype,"value").set.call(brief,"Fix the test regression");
  brief.dispatchEvent(new dom.window.Event("input",{bubbles:true}));
  await eventually(()=>assert.equal(brief.value,"Fix the test regression"));
  document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true}));
  await eventually(()=>assert.match(document.body.textContent,/Response lost after submission/));
  document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true}));
  await eventually(()=>assert.equal(submissions.length,2));
  assert.equal(submissions[0].client_request_id,submissions[1].client_request_id,"response loss reuses stable submission ID");
  assert.ok(submissions[0].client_request_id);
  await eventually(()=>assert.equal(window.location.hash,"#/runs/job_created"));
  window.location.hash = "#/repositories";
  await eventually(()=>assert.ok(document.querySelector('a[href="https://github.com/owner/canonical"]')));
  assert.equal(document.querySelector('a[href="https://github.com/repository-alias"]'),null);


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
