import assert from "node:assert/strict";
import test from "node:test";
import { estimatedRunCost, estimatedJobCost, estimatedCostSummary, formatEstimatedCost } from "./run-metrics.js";

const run = (model, token_usage) => ({ model, token_usage });
test("blended costs use recorded total tokens and exact model matches", () => {
  for (const [model, price] of [["gpt-5.6-luna", 1], ["gpt-5.6-terra", 3], ["gpt-5.6-sol", 5], ["haiku", 1], ["sonnet", 5], ["opus", 10]]) {
    assert.equal(estimatedRunCost(run(model, "1000000")), price);
    assert.equal(estimatedRunCost(run(model, "250000")), price / 4);
    assert.equal(estimatedRunCost(run(model, "0")), 0);
  }
  for (const model of [undefined, "", "unknown", "sonnet-new", "toString", "__proto__"]) {
    assert.equal(estimatedRunCost(run(model, "1000000")), null);
  }
});
test("missing and invalid usage cannot become a zero cost", () => {
  for (const tokens of [undefined, null, 0, "", "01", "-1", "1.5", "NaN", "9".repeat(400)]) {
    assert.equal(estimatedRunCost(run("sonnet", tokens)), null);
  }
});
test("job costs include all attempts and exclude incomplete estimates from totals", () => {
  const known = { runs: [run("sonnet", "1000000"), run("opus", "250000")] };
  const unknown = { runs: [run("sonnet", "1000000"), run("unknown", "1000000")] };
  const missing = { runs: [run("sonnet", undefined)] };
  assert.equal(estimatedJobCost(known), 7.5);
  assert.equal(estimatedJobCost(unknown), null);
  assert.equal(estimatedJobCost(missing), null);
  assert.equal(estimatedJobCost({ runs: [] }), null);
  assert.deepEqual(estimatedCostSummary([known, unknown, missing]), { total: 7.5, estimated: 1 });
  assert.deepEqual(estimatedCostSummary([unknown]), { total: null, estimated: 0 });
  assert.deepEqual(estimatedCostSummary([]), { total: null, estimated: 0 });
  assert.deepEqual(estimatedCostSummary([{ runs: [run("haiku", "0")] }]), { total: 0, estimated: 1 });
});
test("USD display distinguishes unavailable, zero and sub-cent costs", () => {
  assert.equal(formatEstimatedCost(null), "Unavailable");
  assert.equal(formatEstimatedCost(0), "$0.00");
  assert.equal(formatEstimatedCost(0.001), "< $0.01");
  assert.equal(formatEstimatedCost(7.5), "$7.50");
});
