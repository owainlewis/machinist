import assert from "node:assert/strict";
import test from "node:test";
import { scaleTop, usageTotals } from "./usage.js";

test("usage totals leave cancelled runs out of the pass rate", () => {
  const totals = usageTotals([{ succeeded: 3, failed: 1, cancelled: 4, tokens: 10, cost: 0.25 }, { succeeded: 0, failed: 0, cancelled: 0, tokens: 5, cost: 0 }]);
  assert.deepEqual(totals, { succeeded: 3, failed: 1, cancelled: 4, tokens: 15, cost: 0.25, judged: 4, passRate: 75 });
  assert.equal(usageTotals([]).passRate, null);
  assert.equal(usageTotals().tokens, 0);
});

test("a chart scale ends on a round number that halves cleanly", () => {
  for (const [value, top] of [[0, 2], [1, 2], [2, 2], [3, 4], [5, 6], [7, 10], [10, 10], [11, 20], [45, 60], [70, 100], [2300000, 4000000]]) assert.equal(scaleTop(value), top, `scaleTop(${value})`);
});
