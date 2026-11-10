import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

const ticket = { id: "t_1", repo: "owner/repo", title: "Fix login", state: "pr_ready", issue_number: 5, pr_number: 7, created_by: "alice" };
const entry = (id, kind, author, body) => ({ id, kind, author, body, created_at: "2026-10-15T19:59:00Z" });

test("a ticket shows which runner took each run, its log and its figures", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { TicketView } = await server.ssrLoadModule("/src/task-detail.jsx");
  const detail = {
    ticket,
    entries: [entry(1, "request", "alice", "Fix the **login**"), entry(2, "message", "machinist", "<script>alert(1)</script>Working on it")],
    runs: [
      { id: "run_1", kind: "agent", status: "succeeded", harness: "claude", runner: "mac-mini", tokens: 1200, cost: 0.5, started_at: "2026-10-15T19:59:00Z", ended_at: "2026-10-15T20:00:00Z" },
      { id: "run_2", kind: "reviewer", reviewer: "review", status: "failed", harness: "claude", runner: "cluster" },
      { id: "run_3", kind: "reviewer", reviewer: "security", status: "queued", harness: "claude" },
    ],
    figures: { ticket_id: "t_1", questions: 0, feedback_rounds: 0, tokens: 1200, elapsed: 1e9, merged: false },
  };
  const html = renderToStaticMarkup(React.createElement(TicketView, { detail }));
  for (const want of [/succeeded on mac-mini/, /Reviewer: review/, /failed on cluster/, /Waiting for a runner/, /1,200 tokens/, /\$0\.50/, /issues\/5/, /pull\/7/, /<strong>login<\/strong>/, /Cancel ticket/, /Feedback rounds/]) assert.match(html, want);
  assert.doesNotMatch(html, /<script>/);
  const ended = renderToStaticMarkup(React.createElement(TicketView, { detail: { ...detail, ticket: { ...ticket, state: "done" } } }));
  assert.doesNotMatch(ended, /Cancel ticket/);
  const waiting = renderToStaticMarkup(React.createElement(TicketView, { detail: { ...detail, entries: [...detail.entries, entry(3, "message", "machinist", "Which database?")], ticket: { ...ticket, state: "needs_you", question: "Which database?" } } }));
  assert.match(waiting, /Which database\?/);
  assert.match(waiting, /Send reply/);
});
