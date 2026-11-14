import assert from "node:assert/strict";
import test from "node:test";
import { sortTickets, isActive, issueLink, prLink, ticketSource } from "./task-presentation.js";

const ticket = (id, state) => ({ id, state });

test("jobs are ordered by who has to act next, then by finished work", () => {
  const sorted = sortTickets([ticket("d", "done"), ticket("a", "working"), ticket("b", "needs_you"), ticket("c", "pr_ready"), ticket("e", "failed")]);
  assert.deepEqual(sorted.map(item => item.id), ["b", "a", "c", "d", "e"]);
});

test("finished work is capped and active work is not", () => {
  assert.deepEqual(sortTickets([]), []);
  const many = Array.from({ length: 40 }, (_, index) => ticket(String(index), "done"));
  assert.equal(sortTickets(many).length, 25);
  assert.equal(sortTickets([...many, ticket("x", "working")])[0].id, "x");
});

test("the three active states count as active", () => {
  for (const state of ["working", "needs_you", "pr_ready"]) assert.equal(isActive({ state }), true);
  for (const state of ["done", "failed", "cancelled"]) assert.equal(isActive({ state }), false);
});

test("links and sources come from the ticket", () => {
  assert.equal(issueLink({ repo: "o/r", issue_number: 3 }), "https://github.com/o/r/issues/3");
  assert.equal(issueLink({ repo: "o/r" }), "");
  assert.equal(prLink({ repo: "o/r", pr_number: 9 }), "https://github.com/o/r/pull/9");
  assert.equal(ticketSource({ issue_number: 3 }), "Issue #3");
  assert.equal(ticketSource({ source: "schedule:nightly" }), "Automation");
  assert.equal(ticketSource({ source: "api" }), "Assigned here");
});
