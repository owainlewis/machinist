import assert from "node:assert/strict";
import test from "node:test";
import { formatDuration, jobState, newestFirst, prLink, runningTickets } from "./jobs.js";

test("ticket states read as the run API's job states", () => {
  assert.equal(jobState({ state: "working" }), "queued");
  assert.equal(jobState({ state: "working" }, true), "running");
  assert.equal(jobState({ state: "needs_you" }), "needs_input");
  assert.equal(jobState({ state: "pr_ready" }), "done");
  assert.equal(jobState({ state: "done" }), "done");
  assert.equal(jobState({ state: "cancelled" }), "cancelled");
  assert.equal(jobState({ state: "failed" }), "failed");
});

test("jobs list newest first", () => {
  const jobs = [{ id: "a", created_at: "2026-11-17T10:00:00Z" }, { id: "b", created_at: "2026-11-17T12:00:00Z" }];
  assert.deepEqual(newestFirst(jobs).map(job => job.id), ["b", "a"]);
});

test("a job a runner holds is running", () => {
  assert.deepEqual([...runningTickets([{ jobs: [{ ticket_id: "t_1" }] }, {}])], ["t_1"]);
});

test("helpers", () => {
  assert.equal(prLink({ repo: "owner/repo", pr_number: 4 }), "https://github.com/owner/repo/pull/4");
  assert.equal(prLink({ repo: "owner/repo" }), "");
  assert.equal(formatDuration(5088), "5s");
  assert.equal(formatDuration(125000), "2m 5s");
  assert.equal(formatDuration(undefined), "");
});
