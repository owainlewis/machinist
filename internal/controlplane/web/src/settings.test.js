import assert from "node:assert/strict";
import test from "node:test";
import { checkPage, dayLabel, githubLine, githubWarnings, pollLabel, roleLabel, settingsPages, tokenLine } from "./settings.js";

test("the side nav lists one page per topic, overview first", () => {
  assert.deepEqual(settingsPages.map(([page]) => page), ["overview", "projects", "github", "linear", "tokens", "presets"]);
});

test("a setup check links to the page that holds more about it", () => {
  assert.equal(checkPage("GitHub"), "github");
  assert.equal(checkPage("Projects"), "projects");
  assert.equal(checkPage("Agents"), "presets");
  assert.equal(checkPage("Runner"), "");
});

test("the poll interval reads in minutes and seconds", () => {
  assert.equal(pollLabel(60), "Every 1m");
  assert.equal(pollLabel(90), "Every 1m 30s");
  assert.equal(pollLabel(15), "Every 15s");
  assert.equal(pollLabel(0), "");
});

test("roles and token counts read as words", () => {
  assert.equal(roleLabel("operator")[0], "Person");
  assert.equal(roleLabel("runner")[0], "Runner");
  assert.equal(roleLabel("all")[0], "Person and runner");
  assert.equal(tokenLine([{}, {}]), "2 active tokens.");
  assert.equal(tokenLine([{}, { revoked_at: "2026-11-20T00:00:00Z" }]), "1 active token, 1 revoked.");
  assert.equal(dayLabel("2026-11-20T12:00:00Z"), "Nov 20, 2026");
  assert.equal(dayLabel("nope"), "");
});

test("a repository with a warning or a problem counts once", () => {
  assert.equal(githubWarnings({ repositories: [{ warnings: [] }, { warnings: ["a", "b"] }, { warnings: [], problem: "x" }] }), 2);
  assert.equal(githubWarnings(null), 0);
});

test("the GitHub page says how runs reach GitHub", () => {
  assert.equal(githubLine({ repositories: [] }), "Runs use each runner's own GH_TOKEN.");
  assert.equal(githubLine({ app: { id: 1 }, repositories: [{ warnings: ["x"] }] }), "Runs use short-lived tokens from the GitHub App. 1 repository needs a look.");
});
