import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const automation = { name: "review-open-prs", repo: "owner/repo", agent: "maintainer", schedule: "0 9 * * 1-5", timezone: "UTC", next_due: "2026-10-16T09:00:00Z" };

test("Automations shows the schedule, next due time, last ticket and a run button", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { AutomationsPage } = await server.ssrLoadModule("/src/automations.jsx");
  const html = renderToStaticMarkup(React.createElement(AutomationsPage, { loaded: true, automations: [{ ...automation, last_ticket: { id: "t_1", repo: "owner/repo", state: "done", outcome: "Merged 2 PRs." } }] }));
  for (const want of [/review-open-prs/, /0 9 \* \* 1-5/, /maintainer agent/, /Merged 2 PRs\./, /href="#\/work\/t_1"/, /Run now/]) assert.match(html, want);
  assert.match(renderToStaticMarkup(React.createElement(AutomationsPage, { loaded: true, automations: [] })), /factory\/automations/);
  assert.match(renderToStaticMarkup(React.createElement(AutomationsPage, { loaded: true, automations: [automation] })), /Never run/);
  const active = renderToStaticMarkup(React.createElement(AutomationsPage, { loaded: true, automations: [{ ...automation, last_ticket: { id: "t_1", repo: "owner/repo", state: "working" } }] }));
  const dom = new JSDOM(active);
  assert.equal(dom.window.document.querySelector("button").disabled, true);
  dom.window.close();
});
