import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
test("factory shows real worker permissions, transcript, and current approval version", async (context) => {
  const dom = new JSDOM('<div id="root"></div>', {
      url: "http://localhost/#/factory",
    }),
    prior = new Map();
  for (const name of [
    "window",
    "document",
    "navigator",
    "localStorage",
    "Event",
    "MouseEvent",
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
  const task = {
    id: "task_a",
    title: "Add search",
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
        projects: [{ id: "p", name: "Example" }],
      };
    else if (url === "/api/factory/projects/p")
      body = { session: { id: "foreman" }, tasks: [task] };
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
          role: "builder",
          status: task.status === "failed" ? "failed" : "awaiting_permission",
        },
        events: [{ id: 1, kind: "message_delta", text: "Saved worker output" }],
        permissions: [
          { id: "perm_1", title: "Run unit tests", status: "pending" },
        ],
      };
    else if (url === "/api/factory/tasks/task_a")
      body = { task, diff: "+ search", sessions: [{ id: "worker" }] };
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
  assert.match(document.body.textContent, /Unit tests · Passed/);
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
