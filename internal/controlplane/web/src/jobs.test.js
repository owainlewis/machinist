import assert from "node:assert/strict";
import test from "node:test";
import { boardOf, failureReason, filterJobs, formatDuration, linkParts, jobState, newestFirst, noPullRequest, prChip, prLink, prState, prSummary, quickCounts, runningJobs, runningTickets, sourceLabel, sourceLink, sourceLinkText, sourcesOf } from "./jobs.js";

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

test("a job's pull request says whether it is open, merged or closed", () => {
  assert.equal(prState({ pr_number: 4, pr_state: "open" }), "Open");
  assert.equal(prState({ pr_number: 4, pr_state: "merged" }), "Merged");
  assert.equal(prState({ pr_number: 4, pr_state: "closed" }), "Closed");
  assert.equal(prState({ pr_number: 4 }), "");
  assert.equal(prState({ pr_state: "open" }), "");
  assert.equal(prSummary({ pr_number: 4, pr_state: "merged" }), "#4 · Merged");
  assert.equal(prSummary({ pr_number: 4 }), "#4");
  assert.equal(prSummary({}), "");
});

test("a job that ended without a pull request has none, not none yet", () => {
  assert.equal(noPullRequest("queued"), "None yet");
  assert.equal(noPullRequest("running"), "None yet");
  assert.equal(noPullRequest("done"), "None");
  assert.equal(noPullRequest("failed"), "None");
  assert.equal(noPullRequest("cancelled"), "None");
});

test("a failed job's reason is the last thing Machinist said", () => {
  const entries = [
    { kind: "request", body: "Fix it" },
    { kind: "message", body: "https://github.com/owner/repo/pull/4" },
    { kind: "comment", body: "Try again" },
    { kind: "message", body: "Preset \"gone\" is not in the factory." },
    { kind: "comment", body: "Hm" },
  ];
  assert.equal(failureReason(entries), "Preset \"gone\" is not in the factory.");
  assert.equal(failureReason([{ kind: "request", body: "Fix it" }]), "");
  assert.equal(failureReason(), "");
});

test("links in text are split out so they can be clicked", () => {
  assert.deepEqual(linkParts("Merged: https://github.com/owner/repo/pull/642"), [{ text: "Merged: " }, { href: "https://github.com/owner/repo/pull/642" }]);
  assert.deepEqual(linkParts("See https://example.com/a. Then http://example.com/b, and done"), [
    { text: "See " }, { href: "https://example.com/a" }, { text: ". Then " }, { href: "http://example.com/b" }, { text: ", and done" },
  ]);
  assert.deepEqual(linkParts("(https://example.com/x)"), [{ text: "(" }, { href: "https://example.com/x" }, { text: ")" }]);
  assert.deepEqual(linkParts("no links here, javascript:alert(1)"), [{ text: "no links here, javascript:alert(1)" }]);
  assert.deepEqual(linkParts(""), []);
});

test("a job's source has a label, and a link only when it is a web link", () => {
  assert.equal(sourceLabel("linear"), "Linear");
  assert.equal(sourceLabel("api"), "API");
  assert.equal(sourceLabel("web"), "Web");
  assert.equal(sourceLabel("schedule"), "Schedule");
  assert.equal(sourceLabel("asana"), "Asana", "a tracker added later shows its name");
  assert.equal(sourceLabel(""), "Unknown");
  assert.equal(sourceLink({ source: "linear", source_url: "https://linear.app/x/issue/ENG-1" }), "https://linear.app/x/issue/ENG-1");
  assert.equal(sourceLink({ source: "api" }), "");
  assert.equal(sourceLink({ source: "linear", source_url: "javascript:alert(1)" }), "");
});

test("the source filter offers the sources the jobs came from, in a fixed order", () => {
  assert.deepEqual(sourcesOf([{ source: "linear" }, { source: "api" }, { source: "zendesk" }, { source: "web" }, { source: "linear" }, {}]), ["web", "api", "linear", "zendesk"]);
});

test("filtering by source shows only that source's jobs", () => {
  const tickets = [
    { id: "a", source: "linear", state: "working" },
    { id: "b", source: "api", state: "done" },
    { id: "c", source: "linear", state: "failed" },
  ];
  assert.deepEqual(filterJobs(tickets, { source: "linear" }).map(job => job.id), ["a", "c"]);
  assert.deepEqual(filterJobs(tickets, {}).map(job => job.id), ["a", "b", "c"]);
  assert.deepEqual(filterJobs(tickets, { source: "jira" }), []);
});

test("quick filters pick jobs by state, and count within the source", () => {
  const tickets = [
    { id: "run", source: "linear", state: "working" },
    { id: "queue", source: "linear", state: "working" },
    { id: "review", source: "linear", state: "pr_ready", pr_number: 3, pr_state: "open" },
    { id: "merged", source: "api", state: "done" },
    { id: "bad", source: "api", state: "failed" },
    { id: "stop", source: "web", state: "cancelled" },
  ];
  const running = new Set(["run"]);
  const pick = quick => filterJobs(tickets, { quick }, running).map(job => job.id);
  assert.deepEqual(pick("running"), ["run"]);
  assert.deepEqual(pick("done"), ["review", "merged"]);
  assert.deepEqual(pick("failed"), ["bad"]);
  assert.deepEqual(pick("needs_person"), ["review"]);
  assert.deepEqual(pick("cancelled"), ["stop"]);
  assert.deepEqual(filterJobs(tickets, { source: "linear", quick: "done" }, running).map(job => job.id), ["review"]);
  assert.deepEqual(quickCounts(tickets, "", running), { running: 1, done: 2, failed: 1, needs_person: 1, cancelled: 1 });
  assert.deepEqual(quickCounts(tickets, "linear", running), { running: 1, done: 1, failed: 0, needs_person: 1, cancelled: 0 });
});

test("a job's pull request chip has its number, state and GitHub link", () => {
  assert.deepEqual(prChip({ repo: "owner/repo", pr_number: 4, pr_state: "merged" }), { number: 4, state: "merged", label: "Merged", href: "https://github.com/owner/repo/pull/4" });
  assert.deepEqual(prChip({ repo: "owner/repo", pr_number: 4 }), { number: 4, state: "", label: "", href: "https://github.com/owner/repo/pull/4" });
  assert.equal(prChip({ repo: "owner/repo" }), null);
});

test("a job's original is named by the site it is on", () => {
  assert.equal(sourceLinkText("https://linear.app/team/issue/ENG-1/fix"), "Open in Linear");
  assert.equal(sourceLinkText("https://github.com/owner/repo/issues/7"), "Open in GitHub");
  assert.equal(sourceLinkText("https://acme.atlassian.net/browse/OPS-1"), "Open in Jira");
  assert.equal(sourceLinkText("https://tracker.example.com/1"), "Open in tracker.example.com");
  assert.equal(sourceLinkText("not a link"), "Open the original");
});
