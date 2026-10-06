import assert from "node:assert/strict";
import test from "node:test";
import { routeFromHash, taskHref } from "./routes.js";

test("routeFromHash recognizes task detail routes, including old run links", () => {
  assert.deepEqual(routeFromHash("#/tasks/job_123"), { view: "task", jobID: "job_123" });
  assert.deepEqual(routeFromHash("#/runs/job%2F123"), { view: "task", jobID: "job/123" });
  assert.equal(taskHref("job/123"), "#/tasks/job%2F123");
});

test("old page links land on their new pages", () => {
  for (const [hash, view] of [["#/runs", "tasks"], ["#/triggers", "automations"], ["#/analytics", "usage"], ["#/workflows", "settings"], ["#/commands", "settings"]]) {
    assert.deepEqual(routeFromHash(hash), { view, jobID: "" });
  }
});

test("routeFromHash falls back to home or tasks for incomplete or malformed routes", () => {
  assert.deepEqual(routeFromHash(""), { view: "home", jobID: "" });
  assert.deepEqual(routeFromHash("#/unknown"), { view: "home", jobID: "" });
  assert.deepEqual(routeFromHash("#/runs/"), { view: "tasks", jobID: "" });
  assert.deepEqual(routeFromHash("#/tasks/%E0%A4%A"), { view: "tasks", jobID: "" });
});
