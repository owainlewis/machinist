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
const day = (date, more = {}) => ({ day: date, succeeded: 0, failed: 0, cancelled: 0, tokens: 0, cost: 0, ...more });

test("Home is the assign box, the numbers, then one flat list with a status on each row", async context => {
  const render = await load(context);
  const html = render({
    tickets: [ticket("d", "done"), ticket("w", "working"), ticket("q", "needs_you", { question: "Which database?" }), ticket("p", "pr_ready", { pr_number: 7 }), ticket("f", "failed")],
    runners: [{ name: "mac-mini", slots: 2, last_contact: ago(5), jobs: [{ run_id: "r1", ticket_id: "w", title: "Title w", kind: "agent", started_at: ago(75) }] }],
    queue: { waiting: 1, oldest_queued_at: ago(40) },
    usage: { days: [day("2026-11-12", { succeeded: 3, failed: 1, cancelled: 2, tokens: 1200, cost: 0.5 }), day("2026-11-13", { succeeded: 6, tokens: 2300000 })] },
  });
  const order = ["What needs doing?", "Running now", "Runs per day", "Recent work", "Title q", "Title w", "Title p", "Title d", "Title f"].map(text => html.indexOf(text));
  assert.ok(order.every((at, index) => at >= 0 && (index === 0 || at > order[index - 1])), `order = ${order}`);
  assert.equal(html.match(/<ul class="list"/g).length, 1);
  for (const want of [
    /Which database\?/, /aria-label="Reply to Title q"/, /href="#\/work\/q"/, /href="https:\/\/github.com\/owner\/repo\/pull\/7"/, /PR #7/,
    /Agent on mac-mini, <time[^>]*>1m 1\ds/, /1 run has waited 4\ds for a runner/, /1<span>of 2 slots/, /1 of 1 runners online/,
    /1 PR ready for review/, />90%</, /9 of 10 runs passed/, /2\.3M/, /\$0\.50 reported cost/, /Last 2 days/,
    /<th scope="row">Thu, Nov 12<\/th><td>3<\/td><td>1<\/td>/, /<td>2,300,000<\/td>/,
  ]) assert.match(html, want);
  for (const state of ["Needs you", "Working", "PR ready", "Done", "Failed"]) assert.match(html, new RegExp(`</svg><span>${state}</span></span><time class="row-time"`));
  assert.doesNotMatch(html, /cannot start/);
});

test("Home says when waiting runs have no runner to take them", async context => {
  const render = await load(context);
  const html = render({ tickets: [ticket("w", "working")], runners: [{ name: "lost", last_contact: "2026-01-01T00:00:00Z" }], queue: { waiting: 2, oldest_queued_at: ago(600) } });
  assert.match(html, /2 runs are waiting for a runner, the oldest for 10m/);
  assert.match(html, /No runner is online, so waiting runs cannot start/);
  assert.match(html, /href="#\/runners"/);
  assert.match(html, /0 of 1 runners online/);
});

test("with nothing going on Home says so and the charts are empty", async context => {
  const render = await load(context);
  const quiet = render({ tickets: [], runners: [], queue: { waiting: 0 }, usage: { days: [day("2026-11-13")] } });
  for (const want of [/id="assign-work"/, /No work yet/, /No runs waiting/, /No runner connected/, /No run has finished in this period/, /No tokens used in this period/]) assert.match(quiet, want);
  assert.doesNotMatch(quiet, /class="plot"/);
  assert.doesNotMatch(render({ loaded: false }), /No work yet/);
  assert.match(render({ error: "The server is not responding." }), /role="alert"[^>]*>The server is not responding\./);
});
