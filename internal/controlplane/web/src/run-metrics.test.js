import assert from "node:assert/strict";
import test from "node:test";
import { formatCost, formatDurationMillis, formatTokens, ledgerFigures, runDuration, ticketFigures } from "./run-metrics.js";

test("durations read at the right scale", () => {
  assert.equal(formatDurationMillis(500), "0s");
  assert.equal(formatDurationMillis(61_000), "1m 1s");
  assert.equal(formatDurationMillis(3_660_000), "1h 1m");
  assert.equal(formatDurationMillis(-1), "Unavailable");
  assert.equal(runDuration({ started_at: "2026-01-01T00:00:00Z", ended_at: "2026-01-01T00:00:30Z" }), "30s");
  assert.equal(runDuration({ started_at: "2026-01-01T00:00:00Z" }), "");
});

test("missing usage is not shown as zero", () => {
  assert.equal(formatTokens(undefined), "Not reported");
  assert.equal(formatCost(undefined), "Not reported");
  assert.equal(formatTokens(1234.4), "1,234");
  assert.equal(formatCost(1.5), "$1.50");
});

test("ticket figures show the ledger's fields", () => {
  const rows = Object.fromEntries(ticketFigures({ questions: 1, feedback_rounds: 2, tokens: 300, elapsed: 90e9, merged: false }));
  assert.equal(rows["Questions asked"], "1");
  assert.equal(rows["Feedback rounds"], "2");
  assert.equal(rows["Human commits"], "Counted at merge");
  assert.equal(rows.Cost, "Not reported");
  assert.equal(rows.Elapsed, "1m 30s");
  assert.equal(rows.Merged, "No");
  assert.deepEqual(ticketFigures(undefined), []);
});

test("the ledger is hidden until a PR has merged", () => {
  assert.deepEqual(ledgerFigures(null), []);
  assert.deepEqual(ledgerFigures({ merged_prs: 0 }), []);
  const rows = Object.fromEntries(ledgerFigures({ merged_prs: 2, questions: 0.5, feedback_rounds: 1, human_commits: 2.25, tokens: 1000, cost: 3 }));
  assert.deepEqual(rows, { Questions: "0.5", "Feedback rounds": "1", "Human commits": "2.3", Tokens: "1,000", Cost: "$3.00" });
});
