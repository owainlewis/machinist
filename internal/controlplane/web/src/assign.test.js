import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

async function load(context) {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  return server.ssrLoadModule("/src/assign.jsx");
}

test("one box takes an issue number, an issue link or written work", async context => {
  const { assignRequest } = await load(context);
  assert.deepEqual(assignRequest("#123", "o/r", "id"), { request_id: "id", repo: "o/r", issue_number: 123 });
  assert.deepEqual(assignRequest(" 45 ", "o/r", "id"), { request_id: "id", repo: "o/r", issue_number: 45 });
  assert.deepEqual(assignRequest("https://github.com/a/b/issues/7", "o/r", "id"), { request_id: "id", repo: "a/b", issue_number: 7 });
  assert.deepEqual(assignRequest("# Add dark mode\n\nAll pages", "o/r", "id"), { request_id: "id", repo: "o/r", title: "Add dark mode", body: "# Add dark mode\n\nAll pages" });
  assert.equal(assignRequest("x".repeat(300), "o/r", "id").title.length, 100);
});

test("a PR link is treated as written work, not an issue", async context => {
  const { assignRequest } = await load(context);
  assert.equal(assignRequest("https://github.com/a/b/pull/7", "o/r", "id").issue_number, undefined);
});

test("with no repository the box says how to connect one", async context => {
  const { AssignBox } = await load(context);
  assert.match(renderToStaticMarkup(React.createElement(AssignBox, { repositories: [] })), /Connect a repository/);
  const html = renderToStaticMarkup(React.createElement(AssignBox, { repositories: ["o/r", "o/s"] }));
  assert.match(html, /id="assign-work"/);
  assert.match(html, /<option value="o\/s">/);
});
