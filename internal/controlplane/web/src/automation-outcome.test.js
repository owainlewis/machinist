import assert from "node:assert/strict";
import test from "node:test";
import { automationOutcome, outcomeText } from "./automation-outcome.js";

const withTicket = patch => ({ last_ticket: { id: "t_1", repo: "o/r", ...patch } });

test("an automation that never ran says so", () => {
  assert.deepEqual(automationOutcome({}), { text: "Never run" });
});

test("each state reads from the ticket", () => {
  assert.equal(automationOutcome(withTicket({ state: "working" })).text, "Running now.");
  assert.equal(automationOutcome(withTicket({ state: "working" })).active, true);
  assert.equal(automationOutcome(withTicket({ state: "done", outcome: "Merged two PRs." })).text, "Merged two PRs.");
  assert.equal(automationOutcome(withTicket({ state: "done" })).text, "Finished.");
  assert.equal(automationOutcome(withTicket({ state: "failed", outcome: "No token" })).text, "Failed: No token");
  assert.match(automationOutcome(withTicket({ state: "failed" })).text, /Open the ticket/);
  assert.equal(automationOutcome(withTicket({ state: "cancelled" })).text, "Cancelled.");
  assert.equal(automationOutcome(withTicket({ state: "done" })).active, undefined);
});

test("links go to the ticket, or to the PR when one is ready", () => {
  assert.equal(automationOutcome(withTicket({ state: "done" })).link.href, "#/work/t_1");
  const ready = automationOutcome(withTicket({ state: "pr_ready", pr_number: 4 }));
  assert.equal(ready.link.href, "https://github.com/o/r/pull/4");
  assert.equal(ready.link.external, true);
});

test("outcome text is one bounded line", () => {
  assert.equal(outcomeText("a\n\n b"), "a b");
  assert.equal(outcomeText("x".repeat(300)).length, 240);
  assert.equal(outcomeText(undefined), "");
});
