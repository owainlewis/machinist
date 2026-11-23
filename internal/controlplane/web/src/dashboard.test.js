import assert from "node:assert/strict";
import test from "node:test";
import { dayLabel, doneRate, formatMillis, formatPercent, formatRatio, formatTokens, linePoints, niceMax, runsOn, sparkPoints, stepDays, stepSeries, touchParts } from "./dashboard.js";

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

test("a sparkline spans its box, with the largest day at the top", () => {
  assert.deepEqual(sparkPoints([0, 5, 10], 100, 32), [[0, 32], [50, 16], [100, 0]]);
  assert.deepEqual(sparkPoints([0, 0], 100, 32), [[0, 32], [100, 32]], "an empty range lies flat on the floor");
  assert.deepEqual(sparkPoints([4], 100, 32), [], "one day has no shape to draw");
});

test("touches per pull request read as a short number", () => {
  assert.equal(formatRatio(0), "0");
  assert.equal(formatRatio(1.25), "1.3");
  assert.equal(formatRatio(3), "3");
  assert.equal(formatRatio(12.4), "12");
  assert.equal(formatRatio(null), "None yet");
  assert.equal(touchParts({ replies: 1, reruns: 2, reviews: 0 }), "1 reply, 2 reruns, 0 reviews");
});

test("each known step keeps its color, whichever ran most", () => {
  const busy = stepSeries(["review", "implement"]);
  const quiet = stepSeries(["implement", "review", "publish"]);
  assert.deepEqual(busy.map(({ label }) => label), ["Implement", "Review"]);
  assert.equal(busy[0].color, quiet[0].color);
  assert.equal(busy[1].color, quiet[1].color);
  assert.equal(quiet[2].color, "var(--chart-4)");
});

test("other steps take the free colors and the rest fold into Other", () => {
  const series = stepSeries(["implement", "plan", "test", "lint", "docs"]);
  assert.deepEqual(series.map(({ label }) => label), ["Implement", "Plan", "Test", "Lint", "Other"]);
  assert.equal(new Set(series.map(({ color }) => color)).size, 5);
  assert.deepEqual(series.at(-1).names, ["docs"]);
  const days = stepDays([{ day: "2026-11-18", steps: { implement: 2, docs: 1 } }, { day: "2026-11-19", steps: {} }], series);
  assert.deepEqual(days[0], { day: "2026-11-18", "step:implement": 2, "step:plan": 0, "step:test": 0, "step:lint": 0, "step:other": 1 });
  assert.equal(days[1]["step:implement"], 0);
  assert.deepEqual(stepSeries([]), []);
});

test("a line chart's points sit mid-column, with the top at the chart's top", () => {
  assert.deepEqual(linePoints([0, 5, 10], 10), [[16.67, 100], [50, 50], [83.33, 0]]);
  assert.deepEqual(linePoints([0, 0], 0), [[25, 100], [75, 100]], "an empty range lies on the floor");
});
