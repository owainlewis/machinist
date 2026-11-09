import assert from "node:assert/strict";
import test from "node:test";
import { groupTickets, isActive, issueLink, prLink, ticketSource } from "./task-presentation.js";

const ticket = (id, state) => ({ id, state });

test("tickets are grouped by who has to act next", () => {
  const groups = groupTickets([ticket("a", "working"), ticket("b", "needs_you"), ticket("c", "pr_ready"), ticket("d", "done"), ticket("e", "failed"), ticket("f", "cancelled")]);
  assert.deepEqual(groups.map(group => [group.id, group.tickets.map(item => item.id)]), [["you", ["b"]], ["merge", ["c"]], ["agent", ["a"]], ["finished", ["d", "e", "f"]]]);
});

test("empty groups take no space and finished work is capped", () => {
  assert.deepEqual(groupTickets([]), []);
  const groups = groupTickets(Array.from({ length: 40 }, (_, index) => ticket(String(index), "done")));
  assert.equal(groups.length, 1);
  assert.equal(groups[0].tickets.length, 25);
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
