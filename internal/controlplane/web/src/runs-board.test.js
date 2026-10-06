import assert from "node:assert/strict";
import test from "node:test";
import { boardColumnForState, firstLine, githubIssueReference, groupJobs, groupJobsByBoardColumn, jobDisplayTitle, needsAttention, pullRequestURL, searchJobs, statusGroup, taskReason } from "./runs-board.js";

const states = ["queued", "running", "awaiting_approval", "blocked", "interrupted", "failed", "timed_out", "succeeded", "cancelled", "unexpected_state"];

test("every job state lands in exactly one list group", () => {
  assert.deepEqual(Object.fromEntries(states.map((state) => [state, statusGroup(state)])), {
    queued: "queued", running: "running",
    awaiting_approval: "needs", blocked: "needs", interrupted: "needs",
    failed: "failed", timed_out: "failed", unexpected_state: "failed",
    succeeded: "done", cancelled: "done",
  });
  const grouped = groupJobs(states.map((state) => ({ id: state, state })));
  assert.equal(Object.values(grouped).flat().length, states.length);
});

test("the board puts failures with the other states that need you", () => {
  for (const state of ["failed", "timed_out", "awaiting_approval", "blocked", "interrupted"]) {
    assert.equal(boardColumnForState(state), "needs");
    assert.equal(needsAttention(state), true);
  }
  assert.equal(needsAttention("cancelled"), false);
  const columns = groupJobsByBoardColumn(states.map((state) => ({ id: state, state })));
  assert.deepEqual(Object.keys(columns), ["queued", "running", "needs", "done"]);
  assert.deepEqual(columns.done.map(({ id }) => id), ["succeeded", "cancelled"]);
});

test("queued tasks explain what they are waiting for", () => {
  const job = { state: "queued", repository: "neo", runs: [{ state: "queued", command: "build" }] };
  assert.deepEqual(taskReason(job, []), { text: "Waiting for a worker to come online", tone: "warning" });
  assert.deepEqual(taskReason(job, [{ connected: true, repositories: ["machinist"] }]), { text: "No online worker has neo", tone: "warning" });
  assert.deepEqual(taskReason(job, [{ connected: true, repositories: ["neo"] }]), { text: "Waiting for a free worker", tone: "" });
  assert.deepEqual(taskReason(job, [{ connected: false, repositories: ["neo"] }]).tone, "warning");
});

test("reasons name the worker, the failure, and the opened pull request", () => {
  assert.equal(taskReason({ state: "running", runs: [{ state: "running", command: "build", worker_name: "laptop" }] }).text, "Running on laptop");
  assert.deepEqual(taskReason({ state: "failed", runs: [{ state: "failed", error: "gh is not logged in\nmore detail" }] }), { text: "gh is not logged in", tone: "danger" });
  assert.equal(taskReason({ state: "interrupted", runs: [{}] }).tone, "warning");
  const shipped = { state: "succeeded", runs: [{ state: "succeeded", summary: "Opened https://github.com/o/r/pull/501 after tests passed" }] };
  assert.equal(taskReason(shipped).text, "PR #501 opened");
  assert.equal(pullRequestURL(shipped), "https://github.com/o/r/pull/501");
  const staged = { state: "running", workflow: { steps: ["plan", "build"] }, runs: [{ state: "running", command: "build" }] };
  assert.equal(taskReason(staged).text, "Build: Running");
});

test("search matches title, id and repository", () => {
  const jobs = [{ id: "job_a", repository: "neo", prompt: "Add a JSON flag" }, { id: "job_b", repository: "machinist", prompt: "Fix heartbeat" }];
  assert.deepEqual(searchJobs(jobs, "json").map(({ id }) => id), ["job_a"]);
  assert.deepEqual(searchJobs(jobs, "MACHINIST").map(({ id }) => id), ["job_b"]);
  assert.deepEqual(searchJobs(jobs, "  ").map(({ id }) => id), ["job_a", "job_b"]);
});

test("GitHub issue titles are preferred over prompts and hashes", () => {
  const job = { id: "job_12345678", prompt: "Complete https://github.com/o/r/issues/7", github_issue_title: "Make cards readable", trigger_subject: "https://github.com/o/r/issues/7" };
  assert.equal(jobDisplayTitle(job), "Make cards readable");
  assert.equal(githubIssueReference(job), "#7");
  assert.equal(jobDisplayTitle({ id: "job_12345678", prompt: "Run an audit" }), "Run an audit");
});

test("reasons are plain text even when agents write markdown", () => {
  assert.equal(firstLine("```\nMem0 Active | noise\n```\n\n`FOREMAN phase=planning`\n\nmore"), "FOREMAN phase=planning");
  assert.equal(firstLine("## **Done:** see [PR 7](https://github.com/o/r/pull/7)"), "Done: see PR 7");
  assert.equal(firstLine("- first item\n- second"), "first item");
  assert.equal(firstLine("---\n\n> quoted"), "quoted");
  assert.equal(firstLine(undefined), "");
});
