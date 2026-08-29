import assert from "node:assert/strict";
import test from "node:test";
import { analyticsState } from "./analytics-state.js";
import { createStatusLoader } from "./status-loader.js";

const measuredJob = { created_at: "2026-08-25T10:00:00Z", state: "succeeded", runs: [{
  id: "run_latest",
  command: "build",
  completed_at: "2026-08-25T12:00:00Z",
  duration_millis: 1250,
  token_usage: "4321",
}] };
const now = new Date("2026-08-25T15:00:00Z");

test("analytics shows loading instead of empty metrics before status loads", () => {
  assert.deepEqual(analyticsState({ jobs: [], days: "30", loaded: false, error: "", now }), { kind: "loading" });
});

test("analytics shows an initial status failure instead of empty metrics", () => {
  assert.deepEqual(analyticsState({ jobs: [], days: "30", loaded: false, error: "Status request failed (503)", now }), {
    kind: "error",
    message: "Status request failed (503)",
  });
});

test("analytics hides prior metrics when a refresh fails", () => {
  assert.deepEqual(analyticsState({ jobs: [measuredJob], days: "30", loaded: true, error: "network unavailable", now }), {
    kind: "error",
    message: "network unavailable",
  });
});

test("analytics presents measured runs from successful status data", () => {
  const state = analyticsState({ jobs: [measuredJob], days: "30", loaded: true, error: "", now });
  assert.equal(state.kind, "ready");
  assert.deepEqual(state.runs.map((run) => run.id), ["run_latest"]);
});

test("analytics presents the empty state only after a successful status response", () => {
  const state = analyticsState({ jobs: [], days: "30", loaded: true, error: "", now });
  assert.equal(state.kind, "empty");
  assert.equal(state.metrics.totalTasks, 0);
  assert.deepEqual(state.runs, []);
});

test("an older success cannot replace a newer request failure", async () => {
  const first = deferred();
  const second = deferred();
  const requests = [first.promise, second.promise];
  const applied = [];
  const loader = createStatusLoader({ request: () => requests.shift(), apply: (result) => applied.push(result) });

  const olderRefresh = loader.refresh();
  const newerRefresh = loader.refresh();
  second.reject(new Error("newer request failed"));
  await newerRefresh;
  first.resolve({ jobs: [measuredJob] });
  await olderRefresh;

  assert.deepEqual(applied, [{ kind: "error", message: "newer request failed" }]);
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}
