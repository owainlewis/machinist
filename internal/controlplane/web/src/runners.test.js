import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

test("Runners shows each runner and when it last made contact", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { RunnersPage, runnerOnline } = await server.ssrLoadModule("/src/runners.jsx");
  const now = Date.parse("2026-10-15T20:00:00Z");
  assert.equal(runnerOnline({ last_contact: "2026-10-15T19:59:00Z" }, now), true);
  assert.equal(runnerOnline({ last_contact: "2026-10-15T19:58:00Z" }, now), false);
  const html = renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [{ name: "mac-mini", harnesses: ["claude", "codex"], last_contact: new Date().toISOString() }, { name: "lost", harnesses: ["claude"], last_contact: "2026-01-01T00:00:00Z" }] }));
  for (const want of [/mac-mini/, /claude, codex/, /Online/, /Not responding/, /Last contact/]) assert.match(html, want);
  assert.match(renderToStaticMarkup(React.createElement(RunnersPage, { loaded: true, runners: [] })), /machinist runner start/);
});
