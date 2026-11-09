import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

const ticket = (id, state, more = {}) => ({ id, state, title: `Title ${id}`, repo: "owner/repo", updated_at: "2026-10-15T19:59:00Z", ...more });

test("Work lists tickets by what each waits on and asks the question in place", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { WorkPage } = await server.ssrLoadModule("/src/work.jsx");
  const html = renderToStaticMarkup(React.createElement(WorkPage, { loaded: true, tickets: [ticket("a", "working"), ticket("b", "needs_you", { question: "Which database?" }), ticket("c", "pr_ready", { pr_number: 7 })], ledger: { merged_prs: 2, questions: 1, feedback_rounds: 0, human_commits: 0, tokens: 10 } }));
  assert.ok(html.indexOf("Waiting on you") < html.indexOf("Waiting for a merge"));
  assert.ok(html.indexOf("Waiting for a merge") < html.indexOf("Waiting on an agent"));
  assert.match(html, /Which database\?/);
  assert.match(html, /href="#\/work\/b"/);
  assert.match(html, /PR #7/);
  assert.match(html, /Average per merged PR, across 2/);
  assert.doesNotMatch(html, /Finished/);
  assert.match(renderToStaticMarkup(React.createElement(WorkPage, { loaded: true, tickets: [] })), /Assign some work/);
  assert.match(renderToStaticMarkup(React.createElement(WorkPage, { loaded: false, tickets: [] })), /Loading tickets/);
  assert.match(renderToStaticMarkup(React.createElement(WorkPage, { loaded: true, tickets: [], error: "Request failed" })), /Request failed/);
});
