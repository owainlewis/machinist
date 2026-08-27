import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { completedRuns, formatDurationMillis, formatReportingCoverage, formatSuccessRate, formatTaskTokenUsage, formatTokenUsage, runDetails, runModelSummary, taskAnalytics, taskDurationMillis, tasksInWindow, tokenUsageSummary } from "./run-metrics.js";

function localDate(year, month, day, hour = 0) {
  return new Date(year, month - 1, day, hour).toISOString();
}

test("tasksInWindow starts at local midnight and drives completed run detail", () => {
  const now = new Date(2026, 7, 25, 15);
  const jobs = [
    { id: "inside", created_at: localDate(2026, 8, 19), runs: [
      { id: "run_reported", agent: "build", completed_at: localDate(2026, 8, 25, 12), duration_millis: 1250, token_usage: "4321" },
      { id: "run_missing", agent: "review", completed_at: localDate(2026, 8, 18, 11), duration_millis: 500 },
      { id: "run_unmeasured", agent: "plan", completed_at: localDate(2026, 8, 25, 10) },
    ] },
    { id: "outside", created_at: localDate(2026, 8, 18, 23), runs: [{ id: "run_outside", completed_at: localDate(2026, 8, 25), duration_millis: 100 }] },
    { id: "future", created_at: localDate(2026, 8, 25, 16), runs: [{ id: "run_future", completed_at: localDate(2026, 8, 25), duration_millis: 100 }] },
  ];
  assert.deepEqual(tasksInWindow(jobs, "7", now).map((job) => job.id), ["inside"]);
  const runs = completedRuns(jobs, "7", now);
  assert.deepEqual(runs.map((run) => run.id), ["run_reported", "run_unmeasured", "run_missing"]);
  assert.equal(runs[0].token_usage, "4321");
  assert.equal(runs[1].token_usage, undefined);
  assert.equal(runs[1].duration_millis, undefined);
});

test("tasksInWindow applies the 30-day boundary to task creation time", () => {
  const now = new Date(2026, 7, 30, 15);
  const jobs = [
    { id: "first-day", created_at: localDate(2026, 8, 1), runs: [] },
    { id: "previous-day", created_at: localDate(2026, 7, 31, 23), runs: [] },
  ];
  assert.deepEqual(tasksInWindow(jobs, "30", now).map((job) => job.id), ["first-day"]);
});

test("taskAnalytics calculates task outcomes and averages complete terminal timings", () => {
  const created_at = localDate(2026, 8, 25, 9);
  const jobs = [
    { id: "success", state: "succeeded", created_at, runs: [{ state: "succeeded", duration_millis: 1000 }, { state: "succeeded", duration_millis: 3000 }] },
    { id: "failed", state: "failed", created_at, runs: [{ state: "failed", duration_millis: 2000 }, { state: "skipped" }] },
    { id: "running", state: "running", created_at, runs: [{ state: "running", duration_millis: 500 }] },
    { id: "queued", state: "queued", created_at, runs: [{ state: "pending" }] },
  ];
  const metrics = taskAnalytics(jobs, "7", new Date(2026, 7, 25, 15));
  assert.equal(metrics.totalTasks, 4);
  assert.equal(metrics.successRate, 0.5);
  assert.equal(metrics.failedTasks, 1);
  assert.equal(metrics.activeTasks, 2);
  assert.equal(metrics.averageTaskDurationMillis, 3000);
  assert.equal(metrics.contributingTasks, 2);
});

test("taskAnalytics excludes terminal tasks with any missing or invalid non-skipped duration", () => {
  const created_at = localDate(2026, 8, 25, 9);
  const jobs = [
    { state: "succeeded", created_at, runs: [{ state: "succeeded", duration_millis: 1000 }, { state: "succeeded" }] },
    { state: "failed", created_at, runs: [{ state: "failed", duration_millis: -1 }] },
    { state: "succeeded", created_at, runs: [{ state: "skipped" }] },
  ];
  const metrics = taskAnalytics(jobs, "7", new Date(2026, 7, 25, 15));
  assert.equal(metrics.averageTaskDurationMillis, null);
  assert.equal(metrics.contributingTasks, 0);
  assert.equal(metrics.successRate, 2 / 3);
});

test("taskDurationMillis excludes skipped pipeline steps", () => {
  assert.equal(taskDurationMillis([{ state: "failed", duration_millis: 1250 }, { state: "skipped" }]), 1250);
  assert.equal(taskDurationMillis([{ state: "failed" }, { state: "skipped" }]), undefined);
  assert.equal(taskDurationMillis([{ state: "skipped" }]), undefined);
});

test("taskAnalytics returns explicit zero counts and unavailable rates for no data", () => {
  const metrics = taskAnalytics([], "30", new Date(2026, 7, 25, 15));
  assert.deepEqual(metrics, { tasks: [], totalTasks: 0, successRate: null, failedTasks: 0, activeTasks: 0, averageTaskDurationMillis: null, contributingTasks: 0 });
  assert.equal(formatDurationMillis(metrics.averageTaskDurationMillis), "Unavailable");
  assert.equal(formatSuccessRate(metrics.successRate), "Unavailable");
});

test("formatters distinguish explicitly reported zero from unavailable usage", () => {
  assert.equal(formatDurationMillis(1250), "1.25s");
  assert.equal(formatDurationMillis(3_661_000), "1h 1m 1s");
  assert.equal(formatSuccessRate(2 / 3), "66.7%");
  assert.equal(formatSuccessRate(Number.NaN), "Unavailable");
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

test("formatTaskTokenUsage marks partial totals as reported", () => {
  assert.equal(formatTaskTokenUsage({ total: undefined, unavailable: 2 }), "Not reported");
  assert.equal(formatTaskTokenUsage({ total: "4321", unavailable: 0 }), "4,321 tokens");
  assert.equal(formatTaskTokenUsage({ total: "4321", unavailable: 1 }), "4,321 tokens reported · 1 step unreported");
  assert.equal(formatTaskTokenUsage({ total: "4321", unavailable: 2 }), "4,321 tokens reported · 2 steps unreported");
});

test("runModelSummary reports every distinct configured model and honest fallbacks", () => {
  assert.equal(runModelSummary([{ model: "gpt-5.6-sol" }, { model: "gpt-5.6-sol" }]), "gpt-5.6-sol");
  assert.equal(runModelSummary([{ model: "deepseek-v4-flash" }, { model: "gpt-5.6-sol" }]), "deepseek-v4-flash · gpt-5.6-sol");
  assert.equal(runModelSummary([{ model: "gpt-5.6-sol" }, {}]), "gpt-5.6-sol · Executor default");
  assert.equal(runModelSummary([{ model: "Not specified" }]), "Not specified");
  assert.equal(runModelSummary([{}]), "Executor default");
  assert.equal(runModelSummary([{ model: "Not specified" }, {}]), "Not specified · Executor default");
  assert.equal(runModelSummary([]), "Executor default");
});

test("runDetails always surfaces the executor, even when a worker has claimed the run", () => {
  const claimed = { executor: "codex", worker_name: "my-macbook", model: "sonnet", duration_millis: 1250, completed_at: "2026-08-25T12:00:00Z", token_usage: "4321" };
  assert.equal(runDetails(claimed), "codex · my-macbook · sonnet · 1.25s · 4,321 tokens");

  const completedWithoutUsage = { executor: "codex", duration_millis: 250, completed_at: "2026-08-25T12:00:00Z" };
  assert.equal(runDetails(completedWithoutUsage), "codex · 250ms · Token usage unavailable");

  const unassigned = { executor: "claude", model: "opus" };
  assert.equal(runDetails(unassigned), "claude · opus");
});

test("analytics presents task KPIs while retaining completed run metrics", async () => {
  const source = await readFile(new URL("./analytics.jsx", import.meta.url), "utf8");
  for (const label of ["Average task time", "Total tasks", "Success rate", "Failed tasks", "Active tasks", "Total reported tokens", "Reporting coverage", "Duration", "Reported token usage"]) {
    assert.match(source, new RegExp(label));
  }
});

test("task detail keeps executor, model, and usage reporting explicit", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /run\.worker_name\s*\|\|\s*run\.executor/);
  assert.match(source, /tokenUsageSummary\(job\.runs\)/);
  const taskDetail = source.match(/function TaskDetail[\s\S]+?function DetailMetric/)?.[0];
  assert.ok(taskDetail);
  assert.match(taskDetail, /label="Executor" value=\{run\.executor\}/);
  assert.match(taskDetail, /run\.model \|\| "Executor default"/);
  assert.match(taskDetail, /"Not reported"/);
});

test("board cards are compact links while list rows retain model and usage", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  const runCard = source.match(/function RunCard[\s\S]+?function RunRow/)?.[0];
  assert.ok(runCard);
  assert.match(runCard, /href=\{`#\/runs\/\$\{encodeURIComponent\(job\.id\)\}`\}/);
  assert.doesNotMatch(runCard, /Progress|Tokens|Submitted/);
  assert.match(runCard, /<State value=\{job\.state\}/);
  assert.doesNotMatch(runCard, /Needs attention/);

  const runRow = source.match(/function RunRow[\s\S]+?function State/)?.[0];
  assert.ok(runRow);
  assert.match(runRow, /runModelSummary\(job\.runs\)/);
  assert.match(runRow, /tokenUsageSummary\(job\.runs\)/);
  assert.match(runRow, /tokens/);
});

test("task detail provides deletion and complete step metadata", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  const taskDetail = source.match(/function TaskDetail[\s\S]+?function DetailMetric/)?.[0];
  assert.ok(taskDetail);
  for (const label of ["Prompt", "Execution", "Repository", "Run with", "Requested model", "Duration", "Token usage", "Executor", "Worker", "Started", "Completed", "Exit code", "Error"]) {
    assert.match(taskDetail, new RegExp(label));
  }
  assert.match(source, /method: "DELETE"/);
  assert.match(source, /"X-Machinist-CSRF": status\.csrf_token/);
  assert.match(source, /Delete task/);
});
