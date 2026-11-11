import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

async function load(context) {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { HomePage } = await server.ssrLoadModule("/src/home.jsx");
  return props => renderToStaticMarkup(React.createElement(HomePage, { repositories: ["owner/repo"], loaded: true, ...props }));
}

const ago = seconds => new Date(Date.now() - seconds * 1000).toISOString();
const ticket = (id, state, more = {}) => ({ id, state, title: `Title ${id}`, repo: "owner/repo", updated_at: ago(60), ...more });

test("Home puts the assign box first, then what needs someone, in order", async context => {
  const render = await load(context);
  const html = render({
    tickets: [ticket("w", "working"), ticket("q", "needs_you", { question: "Which database?" }), ticket("p", "pr_ready", { pr_number: 7 }), ticket("d", "done"), ticket("f", "failed")],
    runners: [{ name: "mac-mini", last_contact: ago(5), jobs: [{ run_id: "r1", ticket_id: "w", title: "Title w", kind: "agent", started_at: ago(75) }] }],
    queue: { waiting: 1, oldest_queued_at: ago(40) },
  });
  const order = ["What needs doing?", "Needs you", "Ready for review", "Running now", "Recently finished"].map(text => html.indexOf(text));
  assert.ok(order.every((at, index) => at >= 0 && (index === 0 || at > order[index - 1])), `order = ${order}`);
  for (const want of [/Which database\?/, /aria-label="Reply to Title q"/, /href="#\/work\/q"/, /href="https:\/\/github.com\/owner\/repo\/pull\/7"/, /PR #7/, /mac-mini/, /1m 1\ds/, /1 run has waited 4\ds for a runner/, /Title d/, /Title f/]) assert.match(html, want);
  assert.doesNotMatch(html, /cannot start/);
  assert.doesNotMatch(html, /data-quiet/);
});

test("Home says when waiting runs have no runner to take them", async context => {
  const render = await load(context);
  const html = render({ tickets: [ticket("w", "working")], runners: [{ name: "lost", last_contact: "2026-01-01T00:00:00Z" }], queue: { waiting: 2, oldest_queued_at: ago(600) } });
  assert.match(html, /2 runs are waiting for a runner, the oldest for 10m/);
  assert.match(html, /No runner is online, so waiting runs cannot start/);
  assert.match(html, /href="#\/runners"/);
});

test("with nothing going on Home is only the assign box", async context => {
  const render = await load(context);
  const quiet = render({ tickets: [], runners: [], queue: { waiting: 0 } });
  assert.match(quiet, /data-quiet/);
  assert.match(quiet, /id="assign-work"/);
  assert.doesNotMatch(quiet, /<section/);
  assert.doesNotMatch(render({ loaded: false }), /data-quiet/);
  assert.match(render({ error: "The server is not responding." }), /role="alert"[^>]*>The server is not responding\./);
});
