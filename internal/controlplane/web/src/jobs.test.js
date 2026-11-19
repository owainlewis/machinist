import assert from "node:assert/strict";
import test from "node:test";
import { boardOf, formatDuration, jobState, newestFirst, prLink, runningJobs, runningTickets } from "./jobs.js";

test("ticket states read as the run API's job states", () => {
  assert.equal(jobState({ state: "working" }), "queued");
  assert.equal(jobState({ state: "working" }, true), "running");
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

test("the board puts each job in Queued, Running or Done", () => {
  const at = minutes => new Date(Date.UTC(2026, 10, 19, 12, minutes)).toISOString();
  const tickets = [
    { id: "q_new", state: "working", created_at: at(5), updated_at: at(5) },
    { id: "q_old", state: "working", created_at: at(1), updated_at: at(1) },
    { id: "r", state: "working", created_at: at(2), updated_at: at(2) },
    { id: "ok", state: "done", created_at: at(0), updated_at: at(3) },
    { id: "bad", state: "failed", created_at: at(0), updated_at: at(4) },
    { id: "stop", state: "cancelled", created_at: at(0), updated_at: at(1) },
  ];
  const board = boardOf(tickets, new Set(["r"]));
  assert.deepEqual(board.queued.map(job => job.ticket.id), ["q_old", "q_new"], "the next to run is on top");
  assert.deepEqual(board.running.map(job => job.ticket.id), ["r"]);
  assert.deepEqual(board.done.map(job => [job.ticket.id, job.state]), [["bad", "failed"], ["ok", "done"], ["stop", "cancelled"]]);
});

test("each running job carries the step it is on and its last action", () => {
  const job = { ticket_id: "t_1", step: "work", action: "Run go test ./..." };
  const found = runningJobs([{ jobs: [job] }, { jobs: [{ ticket_id: "t_2" }] }, {}]);
  assert.equal(found.get("t_1"), job);
  assert.deepEqual([...found.keys()], ["t_1", "t_2"]);
});
