import assert from "node:assert/strict";
import test from "node:test";
import { jobHref, routeFromHash } from "./routes.js";

test("the dashboard, jobs, the new job form and runners are addressable", () => {
  for (const view of ["dashboard", "jobs", "new", "runners"]) assert.deepEqual(routeFromHash(`#/${view}`), { view, jobID: "" });
});

test("jobs is the default and unknown routes return to it", () => {
  for (const hash of ["", "#/", "#/unknown", "#/home", "#/work", "#/jobs/"]) assert.deepEqual(routeFromHash(hash), { view: "jobs", jobID: "" });
});

test("a job has its own route", () => {
  assert.deepEqual(routeFromHash("#/jobs/t_123"), { view: "job", jobID: "t_123" });
  assert.deepEqual(routeFromHash(jobHref("t/1")), { view: "job", jobID: "t/1" });
});

test("a malformed job route opens the list", () => {
  assert.deepEqual(routeFromHash("#/jobs/%E0%A4%A"), { view: "jobs", jobID: "" });
});
