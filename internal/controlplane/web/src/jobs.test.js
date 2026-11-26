import assert from "node:assert/strict";
import test from "node:test";
import { boardDoneLimit, boardOf, failureReason, filterJobs, formatDuration, isFinished, linkParts, listPageSize, jobState, mergeGate, mergeGateDecision, mergeGateRisk, newestFirst, noPullRequest, orderSources, paginationOf, prChip, prLink, prState, prSummary, quickCounts, runningJobs, runningTickets, sourceLabel, sourceLink, sourceLinkText, sourcesOf, stateLabels, ticketsPath } from "./jobs.js";

test("ticket states read as the UI's job states, keeping pr_ready distinct from done", () => {
  assert.equal(jobState({ state: "working" }), "queued");
  assert.equal(jobState({ state: "working" }, true), "running");
  assert.equal(jobState({ state: "pr_ready" }), "needs_you");
  assert.equal(jobState({ state: "done" }), "done");
  assert.equal(jobState({ state: "cancelled" }), "cancelled");
  assert.equal(jobState({ state: "failed" }), "failed");
  assert.equal(stateLabels.needs_you, "Needs you");
  assert.equal(isFinished("needs_you"), true);
  assert.equal(isFinished("queued"), false);
  assert.equal(isFinished("running"), false);
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

test("the board puts each job in Queued, Running, Review or Done", () => {
  const at = minutes => new Date(Date.UTC(2026, 10, 19, 12, minutes)).toISOString();
  const tickets = [
    { id: "q_new", state: "working", created_at: at(5), updated_at: at(5) },
    { id: "q_old", state: "working", created_at: at(1), updated_at: at(1) },
    { id: "r", state: "working", created_at: at(2), updated_at: at(2) },
    { id: "ok", state: "done", created_at: at(0), updated_at: at(3) },
    { id: "review", state: "pr_ready", created_at: at(0), updated_at: at(2) },
    { id: "bad", state: "failed", created_at: at(0), updated_at: at(4) },
    { id: "stop", state: "cancelled", created_at: at(0), updated_at: at(1) },
  ];
  const board = boardOf(tickets, new Set(["r"]));
  assert.deepEqual(board.queued.map(job => job.ticket.id), ["q_old", "q_new"], "the next to run is on top");
  assert.deepEqual(board.running.map(job => job.ticket.id), ["r"]);
  assert.deepEqual(board.needs_you.map(job => [job.ticket.id, job.state]), [["review", "needs_you"]]);
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
  assert.equal(noPullRequest("needs_you"), "None");
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

test("boardOf caps Done to the 20 most recent finished jobs while keeping Queued and Running uncapped", () => {
  const at = minutes => new Date(Date.UTC(2026, 10, 19, 12, minutes)).toISOString();
  const tickets = [];
  const running = new Set();
  for (let i = 0; i < 24; i++) {
    tickets.push({ id: `q_${i}`, state: "working", created_at: at(i), updated_at: at(i) });
  }
  for (let i = 0; i < 22; i++) {
    tickets.push({ id: `r_${i}`, state: "working", created_at: at(i), updated_at: at(i) });
    running.add(`r_${i}`);
  }
  for (let i = 0; i < 30; i++) {
    tickets.push({ id: `d_${i}`, state: i % 2 === 0 ? "done" : "failed", created_at: at(i), updated_at: at(i) });
  }
  const board = boardOf(tickets, running);
  assert.equal(boardDoneLimit, 20);
  assert.equal(board.queued.length, 24, "Queued stays uncapped");
  assert.equal(board.running.length, 22, "Running stays uncapped");
  assert.equal(board.done.length, 20, "Done is capped to 20 cards");
  assert.equal(board.doneTotal, 30, "doneTotal keeps the full finished count");
  assert.equal(board.done[0].ticket.id, "d_29", "most recently updated finished job is first");
  assert.equal(board.done[19].ticket.id, "d_10", "20th most recently updated finished job is last");
});

test("ticketsPath builds board and paginated list queries with filters", () => {
  assert.equal(ticketsPath({ view: "board" }), "/api/v1/tickets?view=board&done_limit=20");
  assert.equal(ticketsPath({ view: "board", source: "linear", quick: "failed" }), "/api/v1/tickets?source=linear&state=failed&view=board&done_limit=20");
  assert.equal(ticketsPath({ view: "list", page: 0 }), `/api/v1/tickets?limit=${listPageSize}&offset=0`);
  assert.equal(ticketsPath({ view: "list", page: 4, pageSize: 50 }), "/api/v1/tickets?limit=50&offset=200");
  assert.equal(ticketsPath({ view: "list", page: 1, pageSize: 25, source: "api", quick: "done" }), "/api/v1/tickets?source=api&state=done%2Cpr_ready&limit=25&offset=25");
  assert.equal(ticketsPath({ view: "list", page: 0, quick: "needs_person" }), `/api/v1/tickets?state=pr_ready&limit=${listPageSize}&offset=0`);
  assert.equal(ticketsPath({ view: "list", page: 0, quick: "running" }), `/api/v1/tickets?state=running&limit=${listPageSize}&offset=0`);
});

test("paginationOf computes page bounds and navigation flags beyond 200 jobs", () => {
  assert.deepEqual(paginationOf({ total: 230, offset: 0, count: 50, pageSize: 50 }), { page: 1, totalPages: 5, start: 1, end: 50, total: 230, hasPrev: false, hasNext: true });
  assert.deepEqual(paginationOf({ total: 230, offset: 200, count: 30, pageSize: 50 }), { page: 5, totalPages: 5, start: 201, end: 230, total: 230, hasPrev: true, hasNext: false });
  assert.deepEqual(paginationOf({ total: 0, offset: 0, count: 0, pageSize: 50 }), { page: 1, totalPages: 1, start: 0, end: 0, total: 0, hasPrev: false, hasNext: false });
  assert.deepEqual(orderSources(["linear", "web", "", "api", "linear"]), ["web", "api", "linear"]);
});

test("merge gate helpers normalize risk levels, decisions, and absent verdicts", () => {
  assert.deepEqual(mergeGateRisk({ merge_gate_risk: "low" }), { level: "low", label: "Low" });
  assert.deepEqual(mergeGateRisk({ merge_gate_risk: "medium" }), { level: "medium", label: "Medium" });
  assert.deepEqual(mergeGateRisk({ merge_gate_risk: " HIGH " }), { level: "high", label: "High" });
  assert.equal(mergeGateRisk({ merge_gate_risk: "unknown" }), null);
  assert.equal(mergeGateRisk({}), null);

  assert.deepEqual(mergeGateDecision({ merge_gate_decision: "approve" }), { value: "approve", label: "Approved" });
  assert.deepEqual(mergeGateDecision({ merge_gate_decision: "human_review" }), { value: "human_review", label: "Human review" });
  assert.equal(mergeGateDecision({ merge_gate_decision: "other" }), null);
  assert.equal(mergeGateDecision({}), null);

  assert.deepEqual(mergeGate({
    merge_gate_decision: "human_review",
    merge_gate_risk: "high",
    merge_gate_reason: "  Touches auth middleware. ",
  }), {
    decision: { value: "human_review", label: "Human review" },
    risk: { level: "high", label: "High" },
    reason: "Touches auth middleware.",
  });
  assert.deepEqual(mergeGate({ merge_gate_risk: "medium" }), {
    decision: null,
    risk: { level: "medium", label: "Medium" },
    reason: "",
  });
  assert.deepEqual(mergeGate({ merge_gate_reason: "Held by policy." }), {
    decision: null,
    risk: null,
    reason: "Held by policy.",
  });
  assert.equal(mergeGate({}), null);
  assert.equal(mergeGate({ merge_gate_decision: "", merge_gate_risk: "  ", merge_gate_reason: "  " }), null);
});
