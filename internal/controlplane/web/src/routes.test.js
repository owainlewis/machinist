import assert from "node:assert/strict";
import test from "node:test";
import { routeFromHash } from "./routes.js";

test("routeFromHash recognizes task detail routes", () => {
  assert.deepEqual(routeFromHash("#/runs/job_123"), { view: "task", jobID: "job_123" });
  assert.deepEqual(routeFromHash("#/runs/job%2F123"), { view: "task", jobID: "job/123" });
});

test("routeFromHash falls back to runs for incomplete or malformed routes", () => {
  assert.deepEqual(routeFromHash("#/runs/"), { view: "runs", jobID: "" });
  assert.deepEqual(routeFromHash("#/runs/%E0%A4%A"), { view: "runs", jobID: "" });
  assert.deepEqual(routeFromHash("#/unknown"), { view: "runs", jobID: "" });
});
