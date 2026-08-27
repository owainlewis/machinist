import assert from "node:assert/strict";
import test from "node:test";
import { boardColumnForState, filterJobs, groupJobsByBoardColumn, needsAttention } from "./runs-board.js";

test("job states map to the three board columns without hiding attention states", () => {
  assert.equal(boardColumnForState("queued"), "planning");
  assert.equal(boardColumnForState("running"), "building");
  assert.equal(boardColumnForState("succeeded"), "ready");

  for (const state of ["failed", "timed_out", "cancelled", "unexpected_state"]) {
    assert.equal(boardColumnForState(state), "building");
    assert.equal(needsAttention(state), true);
  }

  const jobs = ["queued", "running", "succeeded", "failed", "timed_out", "cancelled", "unexpected_state"]
    .map((state) => ({ id: state, state }));
  const grouped = groupJobsByBoardColumn(jobs);
  assert.deepEqual(grouped.planning.map(({ id }) => id), ["queued"]);
  assert.deepEqual(grouped.ready.map(({ id }) => id), ["succeeded"]);
  assert.deepEqual(grouped.building.map(({ id }) => id), ["running", "failed", "timed_out", "cancelled", "unexpected_state"]);
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
  assert.equal(grouped.planning.length + grouped.building.length + grouped.ready.length, visibleJobs.length);
});
