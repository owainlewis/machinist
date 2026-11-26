import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";
import { factoryUpdates, missingSteps, repoName, settingsNeedAttention, setupLine, shouldOpenSetup } from "./setup.js";

const checks = (...states) => ({ checks: states.map(([name, ok, required = true]) => ({ name, ok, required })) });

async function loadSetupPage(context) {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  return server.ssrLoadModule("/src/setup-page.jsx");
}

test("only required checks that fail are missing", () => {
  const setup = checks(["GitHub", true], ["Runner", false], ["Linear", false, false]);
  assert.deepEqual(missingSteps(setup).map(check => check.name), ["Runner"]);
  assert.equal(setupLine(setup), "1 of 2 required steps are done.");
  assert.equal(setupLine(checks(["GitHub", true], ["Linear", false, false])), "Machinist is ready to take jobs.");
});

test("a pasted GitHub link becomes owner/name", () => {
  assert.equal(repoName(" https://github.com/owner/repo/ "), "owner/repo");
  assert.equal(repoName("owner/repo.git"), "owner/repo");
  assert.equal(repoName("owner/my.repo"), "owner/my.repo");
  for (const bad of ["", "owner", "owner/", "a/b/c", "owner/re po"]) assert.equal(repoName(bad), "", bad);
});

test("a new install opens on setup, but a deep link stays where it points", () => {
  const unready = checks(["Runner", false]);
  assert.equal(shouldOpenSetup(unready, ""), true);
  assert.equal(shouldOpenSetup(unready, "#/dashboard"), true);
  assert.equal(shouldOpenSetup(unready, "#/jobs/t_1"), false);
  assert.equal(shouldOpenSetup(checks(["Runner", true]), ""), false);
});

test("out-of-date factory files mark Settings as needing attention without hijacking the start page", () => {
  const ready = { ...checks(["Runner", true]), factory_updates: [{ path: "scripts/publish.sh", status: "outdated" }] };
  assert.equal(factoryUpdates(ready).length, 1);
  assert.equal(settingsNeedAttention(ready), true);
  assert.equal(shouldOpenSetup(ready, "#/dashboard"), false);
  assert.equal(settingsNeedAttention(checks(["Runner", true])), false);
});

test("OverviewPage and ProjectsPage render immediately from initial setup and show layout-matched skeletons on cold load", async context => {
  const { OverviewPage, ProjectsPage } = await loadSetupPage(context);
  const initialSetup = {
    ready: true,
    checks: [
      { name: "GitHub", ok: true, required: true, detail: "Connected as machinist-bot" },
      { name: "Runner", ok: true, required: true, detail: "1 runner online" },
    ],
    projects: [{ repo: "owner/web", source: "app", added_by: "alice" }],
    install_url: "https://github.com/apps/machinistagent/installations/new",
  };

  // Cold loads show skeletons with accessible status labels and preserved header subtitle geometry.
  const coldOverview = renderToStaticMarkup(React.createElement(OverviewPage, { onSignedOut: () => {} }));
  assert.match(coldOverview, /class="page-sub"[^>]*><span class="skeleton skeleton-sub"/);
  assert.match(coldOverview, /class="settings-skeleton" role="status" aria-busy="true"/);
  assert.match(coldOverview, /<span class="sr-only">Checking setup<\/span>/);
  assert.doesNotMatch(coldOverview, /<p class="page-note" role="status">/);

  const coldProjects = renderToStaticMarkup(React.createElement(ProjectsPage, { onSignedOut: () => {} }));
  assert.match(coldProjects, /class="page-sub"[^>]*><span class="skeleton skeleton-sub"/);
  assert.match(coldProjects, /class="settings-skeleton setup-projects" role="status" aria-busy="true"/);
  assert.match(coldProjects, /<span class="sr-only">Loading projects<\/span>/);
  assert.doesNotMatch(coldProjects, /<p class="page-note" role="status">/);

  // Warm loads with initial setup render checklist and projects immediately without skeletons.
  const warmOverview = renderToStaticMarkup(React.createElement(OverviewPage, { setup: initialSetup, onSignedOut: () => {} }));
  assert.match(warmOverview, /Machinist is ready to take jobs\./);
  assert.match(warmOverview, /Connected as machinist-bot/);
  assert.doesNotMatch(warmOverview, /settings-skeleton/);
  assert.doesNotMatch(warmOverview, /Checking setup/);

  const warmProjects = renderToStaticMarkup(React.createElement(ProjectsPage, { setup: initialSetup, onSignedOut: () => {} }));
  assert.match(warmProjects, /1 repository connected\./);
  assert.match(warmProjects, /owner\/web/);
  assert.doesNotMatch(warmProjects, /settings-skeleton/);
  assert.doesNotMatch(warmProjects, /Loading projects/);
});
