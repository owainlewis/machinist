import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
test("factory shows real worker permissions, transcript, and current approval version", async (context) => {
  const dom = new JSDOM('<div id="root"></div>', {
      url: "http://localhost/#/factory",
      pretendToBeVisual: true,
    }),
    prior = new Map();
  for (const name of [
    "window",
    "document",
    "navigator",
    "localStorage",
    "Event",
    "MouseEvent",
    "getComputedStyle",
    "requestAnimationFrame",
    "cancelAnimationFrame",
    "HTMLElement",
    "HTMLInputElement",
    "Node",
    "NodeFilter",
    "MutationObserver",
    "CustomEvent",
  ]) {
    prior.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    Object.defineProperty(globalThis, name, {
      configurable: true,
      writable: true,
      value: dom.window[name],
    });
  }
  for (const name of ["fetch", "EventSource"])
    prior.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
  const streams = [];
  globalThis.EventSource = class {
    constructor() {
      this.listeners = {};
      streams.push(this);
    }
    addEventListener(name, callback) {
      this.listeners[name] = callback;
    }
    close() {}
  };
  dom.window.HTMLElement.prototype.scrollIntoView = function () {};
  const calls = [];
  let appendWorkerOutput = false,
    appendDelayedOutput = false,
    delayDetail = false,
    releaseDetail;
  const task = {
    id: "task_a",
    title: "Add search",
    brief: "Complete long brief: " + "context ".repeat(100),
    design:
      "# Design safety\n\n<script id=unsafe>bad()</script>\n\n[unsafe](javascript:alert)\n\n```js\nconst safe = true;\n```",
    stage: "Review",
    status: "awaiting_approval",
    approval_subject: "code",
    version: 3,
    checks: [{ name: "Unit tests", passed: true, output: "passed" }],
  };
  globalThis.fetch = async (url, options) => {
    calls.push([url, options]);
    let body = {};
    if (url === "/api/factory/status")
      body = {
        enabled: true,
        csrf_token: "csrf",
        foreman: {
          id: "foreman",
          name: "Foreman",
          runtime: "claude",
          model: "",
        },
        projects: [
          { id: "p", name: "Example" },
          { id: "remote", name: "Remote app" },
        ],
        hosts: [
          { id: "local", name: "Local" },
          { id: "vm", name: "Build VM" },
        ],
      };
    else if (url === "/api/factory/projects/remote")
      body = { session: { id: "remote-foreman" }, tasks: [] };
    else if (url.startsWith("/api/factory/sessions/remote-foreman"))
      body = {
        session: { id: "remote-foreman", status: "completed" },
        events: [],
        permissions: [],
      };
    else if (url.startsWith("/api/factory/folders?"))
      body = {
        path: "/home/build/projects",
        parent: "/home/build",
        folders: [{ name: "app", path: "/home/build/projects/app" }],
        truncated: false,
      };
    else if (url === "/api/v1/status")
      body = {
        jobs: [
          {
            id: "old",
            prompt: "Previous batch task",
            state: "succeeded",
            runs: [],
          },
        ],
      };
    else if (url === "/api/factory/projects/p")
      body = {
        session: { id: "foreman" },
        tasks: [
          {
            id: task.id,
            project_id: "p",
            title: task.title,
            brief: task.brief.slice(0, 512),
            stage: task.stage,
            status: task.status,
            activity: task.activity,
            approval_subject: task.approval_subject,
            pending_permissions: task.status === "active" ? 1 : 0,
            version: task.version,
            created_at: "2026-10-04",
            revision: task.revision,
            pr_url: task.pr_url,
            github_error: task.github_error,
            step: task.step,
            check_state: task.check_state,
          },
        ],
      };
    else if (url.startsWith("/api/factory/sessions/foreman"))
      body = {
        session: { id: "foreman", status: "completed" },
        events: [
          {
            id: 1,
            kind: "context",
            title: "Task update",
            text: "Worker completed design",
          },
        ],
        permissions: [],
      };
    else if (url.startsWith("/api/factory/sessions/worker"))
      body = {
        session: {
          id: "worker",
          role: "",
          status: task.status === "failed" ? "failed" : "awaiting_permission",
        },
        events: [
          { id: 1, kind: "message_delta", text: "Saved worker output" },
          ...(appendWorkerOutput
            ? [{ id: 2, kind: "message_delta", text: "Live worker chunk" }]
            : []),
          ...(appendDelayedOutput
            ? [{ id: 3, kind: "message_delta", text: "Chunk during diff read" }]
            : []),
        ],
        permissions: [
          { id: "perm_1", title: "Run unit tests", status: "pending" },
        ],
      };
    else if (url === "/api/factory/tasks/task_a")
      body = {
        task,
        diff: delayDetail ? "+ delayed diff" : "+ search",
        diff_truncated: true,
        files: ["search.js"],
        files_truncated: true,
        sessions: [{ id: "worker" }],
      };
    if (url === "/api/factory/tasks/task_a" && delayDetail)
      await new Promise((resolve) => {
        releaseDetail = resolve;
      });
    return { ok: true, json: async () => body };
  };
  const server = await createServer({
    server: { middlewareMode: true, hmr: false, ws: false },
    appType: "custom",
  });
  let root;
  context.after(async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    await act(async () => root?.unmount());
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
    await server.close();
    dom.window.close();
    for (const [name, d] of prior) {
      if (d) Object.defineProperty(globalThis, name, d);
      else delete globalThis[name];
    }
  });
  root = (await server.ssrLoadModule("/src/main.jsx")).appRoot;
  await eventually(() =>
    assert.match(document.body.textContent, /Task update/),
  );
  assert.equal(
    document.querySelector(".factory-brand").textContent,
    "machinist",
  );
  assert.match(
    document.querySelector(".factory-foreman-model").textContent,
    /Foreman.*Claude Code.*Default model/,
  );
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find((b) =>
        b.textContent.includes("Review code"),
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent.includes("Review code"))
    .click();
  await eventually(() =>
    assert.match(document.body.textContent, /Saved worker output/),
  );
  assert.equal(
    document.querySelector("[role=tab][aria-selected=true]").textContent,
    "Changes",
  );
  const separator = document.querySelector("[role=separator]");
  const beforeWidth = Number(separator.getAttribute("aria-valuenow"));
  separator.dispatchEvent(
    new dom.window.KeyboardEvent("keydown", {
      key: "ArrowLeft",
      bubbles: true,
    }),
  );
  await eventually(() =>
    assert.equal(
      Number(separator.getAttribute("aria-valuenow")),
      beforeWidth + 24,
    ),
  );
  assert.equal(
    localStorage.getItem("machinist-panel-width"),
    String(beforeWidth + 24),
  );
  document.querySelector('button[aria-label="Expand task panel"]').click();
  await eventually(() =>
    assert.ok(document.querySelector(".task-review-panel.expanded")),
  );
  document.querySelector('button[aria-label="Restore task panel"]').click();
  document.querySelector("[role=tab][aria-selected=true]").dispatchEvent(
    new dom.window.KeyboardEvent("keydown", {
      key: "ArrowRight",
      bubbles: true,
    }),
  );
  await eventually(() =>
    assert.equal(
      document.querySelector("[role=tab][aria-selected=true]").textContent,
      "Checks",
    ),
  );
  document
    .querySelector("[role=tab][aria-selected=true]")
    .dispatchEvent(
      new dom.window.KeyboardEvent("keydown", { key: "Home", bubbles: true }),
    );
  await eventually(() =>
    assert.equal(
      document.querySelector(".review-markdown h2").textContent,
      "Design safety",
    ),
  );
  assert.equal(document.querySelector("script#unsafe"), null);
  assert.equal(document.querySelector('a[href^="javascript:"]'), null);
  assert.match(
    document.querySelector(".review-markdown").textContent,
    /<script id=unsafe>/,
  );
  [...document.querySelectorAll("[role=tab]")]
    .find((tab) => tab.textContent === "Changes")
    .dispatchEvent(
      new dom.window.MouseEvent("mousedown", { bubbles: true, button: 0 }),
    );
  assert.equal(
    document.querySelector(".review-agent summary strong").textContent,
    "Run",
  );
  const detailReads = () =>
    calls.filter(
      ([url, options]) =>
        url === "/api/factory/tasks/task_a" && !options?.method,
    ).length;
  const initialDetailReads = detailReads();
  appendWorkerOutput = true;
  streams[0].listeners.changed();
  await eventually(() =>
    assert.match(document.body.textContent, /Live worker chunk/),
  );
  assert.equal(
    detailReads(),
    initialDetailReads,
    "new task array and live chunks do not reload Git details",
  );
  [...document.querySelectorAll("[role=tab]")]
    .find((tab) => tab.textContent === "Design")
    .dispatchEvent(
      new dom.window.MouseEvent("mousedown", { bubbles: true, button: 0 }),
    );
  await eventually(() =>
    assert.ok(document.body.textContent.includes(task.brief)),
  );
  assert.ok(
    document.body.textContent.includes(task.brief),
    "live summaries preserve the complete inspected brief",
  );
  [...document.querySelectorAll("[role=tab]")]
    .find((tab) => tab.textContent === "Changes")
    .dispatchEvent(
      new dom.window.MouseEvent("mousedown", { bubbles: true, button: 0 }),
    );
  task.revision = "new-revision";
  delayDetail = true;
  appendDelayedOutput = true;
  streams[0].listeners.changed();
  await eventually(() => assert.equal(detailReads(), initialDetailReads + 1));
  await eventually(() =>
    assert.match(document.body.textContent, /Chunk during diff read/),
  );
  delayDetail = false;
  releaseDetail();
  await eventually(() =>
    assert.match(document.body.textContent, /delayed diff/),
  );
  assert.match(document.body.textContent, /Chunk during diff read/);
  assert.ok(
    [...document.querySelectorAll("button")].find(
      (button) => button.textContent === "Allow",
    ),
  );

  [...document.querySelectorAll("[role=tab]")]
    .find((tab) => tab.textContent === "Checks")
    .dispatchEvent(
      new dom.window.MouseEvent("mousedown", { bubbles: true, button: 0 }),
    );
  await eventually(() =>
    assert.match(document.body.textContent, /Unit tests · Passed/),
  );
  [...document.querySelectorAll("[role=tab]")]
    .find((tab) => tab.textContent === "Changes")
    .dispatchEvent(
      new dom.window.MouseEvent("mousedown", { bubbles: true, button: 0 }),
    );
  await eventually(() =>
    assert.match(document.body.textContent, /too large to show in full/),
  );
  assert.match(document.body.textContent, /too large to show in full/);
  assert.match(document.body.textContent, /Only part of the file list/);
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Allow")
    .click();
  await eventually(() =>
    assert.ok(
      calls.some(
        ([url, o]) =>
          url === "/api/factory/permissions/perm_1" && JSON.parse(o.body).allow,
      ),
    ),
  );
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find(
        (b) => b.textContent === "Approve delivery" && !b.disabled,
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Approve delivery")
    .click();
  await eventually(() =>
    assert.ok(
      calls.some(
        ([url, o]) =>
          url === "/api/factory/tasks/task_a/approve" &&
          JSON.parse(o.body).version === 3 &&
          o.headers["X-Machinist-CSRF"] === "csrf",
      ),
    ),
  );
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find(
        (b) => b.textContent === "Request changes" && !b.disabled,
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Request changes")
    .click();
  await eventually(() =>
    assert.ok(document.querySelector(".review-approval textarea")),
  );
  const feedbackInput = document.querySelector(".review-approval textarea");
  Object.getOwnPropertyDescriptor(
    dom.window.HTMLTextAreaElement.prototype,
    "value",
  ).set.call(feedbackInput, "Add keyboard support");
  feedbackInput.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find(
        (b) => b.textContent === "Request changes" && !b.disabled,
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Request changes")
    .click();
  await eventually(() =>
    assert.ok(
      calls.some(
        ([url, options]) =>
          url === "/api/factory/tasks/task_a/changes" &&
          JSON.parse(options.body).version === 3 &&
          JSON.parse(options.body).message === "Add keyboard support",
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.getAttribute("aria-label") === "Close task detail")
    .click();
  task.status = "active";
  task.stage = "Build";
  task.activity = "Permission needed";
  streams[0].listeners.changed();
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find((b) =>
        b.textContent.includes("Permission needed →"),
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent.includes("Permission needed →"))
    .click();
  await eventually(() =>
    assert.match(document.body.textContent, /Saved worker output/),
  );
  task.status = "failed";
  task.repairs = 3;
  task.version = 4;
  task.activity = "Repair limit reached";
  streams[0].listeners.changed();
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find(
        (b) => b.textContent === "Continue after review",
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Continue after review")
    .click();
  await eventually(() =>
    assert.ok(
      calls.some(
        ([url, o]) =>
          url === "/api/factory/tasks/task_a/continue" &&
          JSON.parse(o.body).version === 4,
      ),
    ),
  );
  const shell = document.querySelector(".factory-shell");
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Remote app")
    .click();
  await eventually(() =>
    assert.equal(document.querySelector("h1").textContent, "Remote app"),
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Settings")
    .click();
  await eventually(() =>
    assert.equal(document.querySelector("h1").textContent, "Settings"),
  );
  assert.equal(document.querySelector(".factory-inspector"), null);
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "View history")
    .click();
  await eventually(() =>
    assert.match(document.body.textContent, /Previous batch task/),
  );
  assert.equal(document.querySelector(".factory-shell"), shell);
  assert.doesNotMatch(
    document.body.textContent,
    /New task|Analytics|Triggers|Back to factory/,
  );
  [...document.querySelectorAll("button")]
    .find((b) => b.textContent === "Remote app")
    .click();
  await eventually(() =>
    assert.equal(document.querySelector("h1").textContent, "Remote app"),
  );
  assert.equal(document.querySelector(".factory-shell"), shell);
  window.location.hash = "#/runs";
  window.dispatchEvent(new Event("hashchange"));
  await eventually(() =>
    assert.equal(document.querySelector("h1").textContent, "History"),
  );
  assert.equal(document.querySelector(".factory-shell"), shell);
  [...document.querySelectorAll("button")]
    .find((b) => b.getAttribute("aria-label") === "Add project")
    .click();
  await eventually(() => assert.ok(document.getElementById("project-host")));
  assert.equal(document.getElementById("project-host").value, "local");
  assert.match(document.body.textContent, /Build VM/);
  assert.match(document.body.textContent, /Clone once/);
  document.querySelectorAll('input[type="radio"]')[1].click();
  await eventually(() => assert.ok(document.getElementById("project-git-url")));
  assert.match(
    document.body.textContent,
    /Existing folders are never replaced/,
  );
  const projectHost = document.getElementById("project-host");
  Object.getOwnPropertyDescriptor(
    dom.window.HTMLSelectElement.prototype,
    "value",
  ).set.call(projectHost, "vm");
  projectHost.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  [...document.querySelectorAll("button")]
    .find((button) => button.textContent === "Browse folders")
    .click();
  await eventually(() => assert.ok(document.querySelector("[role=dialog]")));
  await eventually(() =>
    assert.ok(
      calls.some(
        ([url, options]) =>
          url.startsWith("/api/factory/folders?host=vm") &&
          options.headers["X-Machinist-CSRF"] === "csrf",
      ),
    ),
  );
  await eventually(() =>
    assert.ok(
      [...document.querySelectorAll("button")].find(
        (button) =>
          button.textContent === "Use this folder" && !button.disabled,
      ),
    ),
  );
  [...document.querySelectorAll("button")]
    .find((button) => button.textContent === "Use this folder")
    .click();
  await eventually(() =>
    assert.equal(
      document.getElementById("project-path").value,
      "/home/build/projects/repository",
    ),
  );
  assert.equal(document.querySelector("[role=dialog]"), null);
});
async function eventually(check) {
  const deadline = Date.now() + 1500;
  while (true) {
    try {
      check();
      return;
    } catch (e) {
      if (Date.now() > deadline) throw e;
      await new Promise((r) => setTimeout(r, 15));
    }
  }
}
