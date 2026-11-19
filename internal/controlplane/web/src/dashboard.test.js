import assert from "node:assert/strict";
import test from "node:test";
import { dayLabel, doneRate, formatMillis, formatPercent, formatTokens, niceMax, runsOn } from "./dashboard.js";

test("chart tops are round numbers above the data", () => {
  assert.equal(niceMax(0), 1);
  assert.equal(niceMax(1), 1);
  assert.equal(niceMax(3), 5);
  assert.equal(niceMax(7), 10);
  assert.equal(niceMax(12), 20);
  assert.equal(niceMax(240), 500);
});

test("numbers read plainly, and missing ones say so", () => {
  assert.equal(formatPercent(0.876), "88%");
  assert.equal(formatPercent(null), "None yet");
  assert.equal(formatMillis(125_000), "2m 5s");
  assert.equal(formatMillis(null), "None yet");
  assert.equal(formatTokens(950), "950");
  assert.equal(formatTokens(12_345), "12k");
  assert.equal(formatTokens(4_100_000), "4.1M");
});

test("a day's runs add up its outcomes", () => {
  assert.equal(runsOn({ succeeded: 3, failed: 1, cancelled: 2 }), 6);
  assert.equal(dayLabel("2026-11-18"), "Nov 18");
});

test("done rate leaves out jobs still running", () => {
  assert.equal(doneRate({ jobs: { total: 5, active: 2, done: 2, failed: 1 } }), 2 / 3);
  assert.ok(Number.isNaN(doneRate({ jobs: { total: 1, active: 1 } })));
});
