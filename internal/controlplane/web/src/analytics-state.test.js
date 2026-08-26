import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

let Analytics;
let vite;

before(async () => {
  vite = await createServer({ appType: "custom", server: { middlewareMode: true } });
  ({ Analytics } = await vite.ssrLoadModule("/src/analytics.jsx"));
});

after(async () => {
  await vite.close();
});

function renderAnalytics(props) {
  return renderToStaticMarkup(React.createElement(Analytics, props));
}

const measuredJobs = [{ runs: [{
  id: "run_measured",
  agent: "build",
  completed_at: new Date().toISOString(),
  duration_millis: 1250,
  token_usage: "4321",
}] }];

test("analytics shows loading before the first successful status response", () => {
  const markup = renderAnalytics({ jobs: [], loaded: false, error: "" });
  assert.match(markup, /Loading…/);
  assert.doesNotMatch(markup, /No completed runs/);
});

test("analytics shows a failure before any successful status response", () => {
  const markup = renderAnalytics({ jobs: [], loaded: false, error: "Status request failed (500)" });
  assert.match(markup, /role="alert"/);
  assert.match(markup, /Status request failed \(500\)/);
  assert.doesNotMatch(markup, /No completed runs/);
});

test("analytics hides prior data when a status refresh fails", () => {
  const markup = renderAnalytics({ jobs: measuredJobs, loaded: true, error: "Network unavailable" });
  assert.match(markup, /role="alert"/);
  assert.match(markup, /Network unavailable/);
  assert.doesNotMatch(markup, /1\.25s|4,321|No completed runs/);
});

test("analytics shows metrics or the empty message after a successful response", () => {
  const metrics = renderAnalytics({ jobs: measuredJobs, loaded: true, error: "" });
  assert.match(metrics, /1\.25s/);
  assert.match(metrics, /4,321/);

  const empty = renderAnalytics({ jobs: [], loaded: true, error: "" });
  assert.match(empty, /No completed runs with measured duration in this window\./);
  assert.doesNotMatch(empty, /Loading…|role="alert"/);
});
