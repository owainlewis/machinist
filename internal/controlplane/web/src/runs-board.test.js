import assert from "node:assert/strict";
import test from "node:test";
import { boardColumnForState, filterJobs, githubIssueReference, groupJobsByBoardColumn, jobDisplayTitle, needsAttention } from "./runs-board.js";

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

test("GitHub issue titles are preferred over prompts and hashes", () => {
  const job = { id: "job_12345678", prompt: "Complete https://github.com/o/r/issues/7", github_issue_title: "Make cards readable", trigger_subject: "https://github.com/o/r/issues/7" };
  assert.equal(jobDisplayTitle(job), "Make cards readable");
  assert.equal(githubIssueReference(job), "#7");
  assert.equal(jobDisplayTitle({ id: "job_12345678", prompt: "Run an audit" }), "Run an audit");
});
