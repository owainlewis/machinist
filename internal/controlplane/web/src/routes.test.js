import assert from "node:assert/strict";
import test from "node:test";
import { jobHref, redirectFor, routeFromHash, settingsHref } from "./routes.js";

test("the dashboard, jobs, the new job form and runners are addressable", () => {
  for (const view of ["dashboard", "jobs", "new", "runners"]) assert.deepEqual(routeFromHash(`#/${view}`), { view, jobID: "" });
});

test("each Settings page has its own route", () => {
  for (const page of ["overview", "projects", "github", "linear", "tokens", "presets"]) {
    assert.deepEqual(routeFromHash(settingsHref(page)), { view: "settings", jobID: "", page });
    assert.equal(redirectFor(settingsHref(page)), "");
  }
});

test("Settings with no page, or one it does not have, opens the overview", () => {
  for (const hash of ["#/settings", "#/settings/", "#/settings/nope"]) {
    assert.deepEqual(routeFromHash(hash), { view: "settings", jobID: "", page: "overview" });
    assert.equal(redirectFor(hash), "#/settings/overview");
  }
});

test("the old setup page redirects to the Settings overview", () => {
  assert.equal(redirectFor("#/setup"), "#/settings/overview");
  assert.equal(redirectFor("#setup"), "#/settings/overview");
  for (const hash of ["", "#/", "#/jobs", "#/jobs/t_1", "#/dashboard"]) assert.equal(redirectFor(hash), "", hash);
});

test("the app opens on the dashboard, and unknown routes return to it", () => {
  for (const hash of ["", "#/", "#/unknown", "#/home", "#/work"]) assert.deepEqual(routeFromHash(hash), { view: "dashboard", jobID: "" });
  assert.deepEqual(routeFromHash("#/jobs/"), { view: "jobs", jobID: "" });
});

test("a job has its own route", () => {
  assert.deepEqual(routeFromHash("#/jobs/t_123"), { view: "job", jobID: "t_123" });
  assert.deepEqual(routeFromHash(jobHref("t/1")), { view: "job", jobID: "t/1" });
});

test("a malformed job route opens the list", () => {
  assert.deepEqual(routeFromHash("#/jobs/%E0%A4%A"), { view: "jobs", jobID: "" });
});
