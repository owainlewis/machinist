import assert from "node:assert/strict";
import test from "node:test";
import { boardColumnForState, filterJobs, groupJobsByBoardColumn, needsAttention } from "./runs-board.js";

test("job states map to the three board columns without hiding attention states", () => {
  assert.equal(boardColumnForState("queued"), "queued");
  assert.equal(boardColumnForState("running"), "running");
  assert.equal(boardColumnForState("succeeded"), "finished");

  for (const state of ["failed", "timed_out", "cancelled", "unexpected_state"]) {
    assert.equal(boardColumnForState(state), "finished");
    assert.equal(needsAttention(state), true);
  }

  const jobs = ["queued", "running", "succeeded", "failed", "timed_out", "cancelled", "unexpected_state"]
    .map((state) => ({ id: state, state }));
  const grouped = groupJobsByBoardColumn(jobs);
  assert.deepEqual(grouped.queued.map(({ id }) => id), ["queued"]);
  assert.deepEqual(grouped.running.map(({ id }) => id), ["running"]);
  assert.deepEqual(grouped.finished.map(({ id }) => id), ["succeeded", "failed", "timed_out", "cancelled", "unexpected_state"]);
});

test("board and table filters use the same filtered job set", () => {
  const jobs = ["queued", "running", "succeeded", "failed", "timed_out", "cancelled", "unexpected_state"]
    .map((state) => ({ id: state, state }));

  assert.deepEqual(filterJobs(jobs, "all").map(({ id }) => id), jobs.map(({ id }) => id));
  assert.deepEqual(filterJobs(jobs, "active").map(({ id }) => id), ["queued", "running"]);
  assert.deepEqual(filterJobs(jobs, "failed").map(({ id }) => id), ["failed", "timed_out"]);
  assert.deepEqual(filterJobs(jobs, "succeeded").map(({ id }) => id), ["succeeded"]);

  const visibleJobs = filterJobs(jobs, "active");
  const grouped = groupJobsByBoardColumn(visibleJobs);
  assert.equal(grouped.queued.length + grouped.running.length + grouped.finished.length, visibleJobs.length);
});
