import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { completedRuns, formatDurationMillis, formatReportingCoverage, formatTokenUsage, runDetails, tokenUsageSummary } from "./run-metrics.js";

test("completedRuns keeps every completed step in the selected window", () => {
  const jobs = [{ runs: [
    { id: "run_reported", agent: "build", completed_at: "2026-08-25T12:00:00Z", duration_millis: 1250, token_usage: "4321" },
    { id: "run_missing", agent: "review", completed_at: "2026-08-25T11:00:00Z", duration_millis: 500 },
    { id: "run_unmeasured", agent: "plan", completed_at: "2026-08-25T10:00:00Z" },
  ] }];
  const runs = completedRuns(jobs, "7", new Date("2026-08-25T15:00:00Z"));
  assert.deepEqual(runs.map((run) => run.id), ["run_reported", "run_missing", "run_unmeasured"]);
  assert.equal(runs[0].token_usage, "4321");
  assert.equal(runs[1].token_usage, undefined);
  assert.equal(runs[2].duration_millis, undefined);
});

test("formatters distinguish explicitly reported zero from unavailable usage", () => {
  assert.equal(formatDurationMillis(1250), "1.25s");
  assert.equal(formatTokenUsage("0"), "0");
  assert.equal(formatTokenUsage("9007199254740993"), "9,007,199,254,740,993");
  assert.equal(formatTokenUsage(9007199254740992), "Unavailable");
  assert.equal(formatTokenUsage(undefined), "Unavailable");
});

test("tokenUsageSummary totals only reported completed steps and tracks coverage", () => {
  const summary = tokenUsageSummary([
    { completed_at: "2026-08-25T12:00:00Z", token_usage: "9007199254740993" },
    { completed_at: "2026-08-25T12:01:00Z", token_usage: "17" },
    { completed_at: "2026-08-25T12:02:00Z" },
    { completed_at: "2026-08-25T12:03:00Z", token_usage: "01" },
    { completed_at: "0001-01-01T00:00:00Z" },
  ]);
  assert.deepEqual(summary, { total: "9007199254741010", reported: 2, completed: 4, unavailable: 2 });
  assert.equal(formatReportingCoverage(summary), "2 of 4 steps");
});

test("tokenUsageSummary does not present missing usage as zero", () => {
  const missing = tokenUsageSummary([{ completed_at: "2026-08-25T12:00:00Z" }]);
  assert.deepEqual(missing, { total: undefined, reported: 0, completed: 1, unavailable: 1 });
  assert.equal(formatTokenUsage(missing.total), "Unavailable");
  assert.equal(formatReportingCoverage(missing), "0 of 1 step");

  const zero = tokenUsageSummary([{ completed_at: "2026-08-25T12:00:00Z", token_usage: "0" }]);
  assert.deepEqual(zero, { total: "0", reported: 1, completed: 1, unavailable: 0 });
  assert.equal(formatTokenUsage(zero.total), "0");
  assert.equal(formatReportingCoverage(tokenUsageSummary([])), "No completed steps");
});

test("runDetails always surfaces the executor, even when a worker has claimed the run", () => {
  const claimed = { executor: "codex", worker_name: "my-macbook", model: "sonnet", duration_millis: 1250, completed_at: "2026-08-25T12:00:00Z", token_usage: "4321" };
  assert.equal(runDetails(claimed), "codex · my-macbook · sonnet · 1.25s · 4,321 tokens");

  const completedWithoutUsage = { executor: "codex", duration_millis: 250, completed_at: "2026-08-25T12:00:00Z" };
  assert.equal(runDetails(completedWithoutUsage), "codex · 250ms · Token usage unavailable");

  const unassigned = { executor: "claude", model: "opus" };
  assert.equal(runDetails(unassigned), "claude · opus");
});

test("analytics presents only duration and reported token usage metrics", async () => {
  const source = await readFile(new URL("./analytics.jsx", import.meta.url), "utf8");
  assert.match(source, /Duration/);
  assert.match(source, /Reported token usage/);
  assert.match(source, /Total reported tokens/);
  assert.match(source, /Reporting coverage/);
  for (const label of ["Executions", "Success rate", "Active", "Outcomes", "Executions over time", "Median duration"]) {
    assert.doesNotMatch(source, new RegExp(label));
  }
});

test("main.jsx no longer drops the executor in favor of the worker name", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /run\.worker_name\s*\|\|\s*run\.executor/);
  assert.match(source, /runDetails/);
  assert.match(source, /tokenUsageSummary\(job\.runs\)/);
  assert.match(source, /unavailable/);

  const runCard = source.match(/function RunCard[\s\S]+?function RunRow/)?.[0];
  assert.ok(runCard);
  assert.match(runCard, /tokenUsageSummary\(job\.runs\)/);
  assert.match(runCard, /Usage/);
  assert.match(runCard, /unavailable/);
});

test("expanded run details keep token usage visible at mobile widths", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  const runSteps = source.match(/function RunSteps[\s\S]+?function State/)?.[0];
  assert.ok(runSteps);

  const details = runSteps.match(/<p className="([^"]+)">\{runDetails\(run\)\}<\/p>/);
  assert.ok(details);
  assert.match(details[1], /\bbreak-words\b/);
  assert.doesNotMatch(details[1], /\btruncate\b/);
});
