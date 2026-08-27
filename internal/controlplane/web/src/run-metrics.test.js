import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { completedRuns, formatDurationMillis, formatSuccessRate, formatTokenUsage, runDetails, taskAnalytics, tasksInWindow } from "./run-metrics.js";

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
  assert.deepEqual(runs.map((run) => run.id), ["run_reported", "run_missing"]);
  assert.equal(runs[0].token_usage, "4321");
  assert.equal(runs[1].token_usage, undefined);
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

test("runDetails always surfaces the executor, even when a worker has claimed the run", () => {
  const claimed = { executor: "codex", worker_name: "my-macbook", model: "sonnet", duration_millis: 1250, completed_at: "2026-08-25T12:00:00Z", token_usage: "4321" };
  assert.equal(runDetails(claimed), "codex · my-macbook · sonnet · 1.25s · 4,321 tokens");

  const unassigned = { executor: "claude", model: "opus" };
  assert.equal(runDetails(unassigned), "claude · opus");
});

test("analytics presents task KPIs while retaining completed run metrics", async () => {
  const source = await readFile(new URL("./analytics.jsx", import.meta.url), "utf8");
  for (const label of ["Average task time", "Total tasks", "Success rate", "Failed tasks", "Active tasks", "Duration", "Reported token usage"]) {
    assert.match(source, new RegExp(label));
  }
});

test("main.jsx no longer drops the executor in favor of the worker name", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /run\.worker_name\s*\|\|\s*run\.executor/);
  assert.match(source, /runDetails/);
});
