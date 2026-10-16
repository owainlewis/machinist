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

test("factory navigation routes remain addressable", () => {
  for (const view of ["agents", "automations", "settings"]) {
    assert.deepEqual(routeFromHash(`#/${view}`), { view, jobID: "" });
  }
});

test("retired inbox links open Runs needing attention", () => {
  assert.deepEqual(routeFromHash("#/inbox"), { view: "runs", jobID: "", filter: "attention" });
});

test("removed workflow configuration routes return to runs",()=>{
 for(const hash of ["#/workflows","#/commands"]) assert.deepEqual(routeFromHash(hash),{view:"runs",jobID:""});
});

test("dashboard is the default page and analytics remains a compatible alias", () => {
  for (const hash of ["", "#/", "#/dashboard", "#/analytics"]) assert.deepEqual(routeFromHash(hash), {view:"dashboard",jobID:""});
});

test("dashboard links open the exact run filter without accepting arbitrary filters", () => {
  for (const filter of ["active", "attention", "pr_ready", "failed", "succeeded"]) {
    assert.deepEqual(routeFromHash(`#/runs?filter=${filter}`),{view:"runs",jobID:"",filter});
  }
  assert.deepEqual(routeFromHash("#/runs?filter=unknown"),{view:"runs",jobID:""});
});
