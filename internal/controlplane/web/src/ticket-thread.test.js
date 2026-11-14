import assert from "node:assert/strict";
import test from "node:test";
import { reportText, ticketThread } from "./ticket-thread.js";

const entry = (id, kind, body, minute) => ({ id, kind, body, author: "alice", created_at: `2026-10-15T20:${minute}:00Z` });
const run = (id, minute) => ({ id, status: "succeeded", started_at: `2026-10-15T20:${minute}:00Z` });

test("a report keeps only what the next message does not repeat", () => {
  assert.equal(reportText("needs_input: Stopped to ask\nQuestion: Which database?"), "Stopped to ask");
  assert.equal(reportText("pr_ready: Opened a PR"), "Opened a PR");
  assert.equal(reportText("Plain text"), "Plain text");
});

test("what the agent does between two things a person says is one turn", () => {
  const messages = ticketThread(
    [entry(1, "request", "Fix it", "00"), entry(2, "report", "needs_input: Stopped\nQuestion: Which?", "05"), entry(3, "message", "Which?", "05"), entry(4, "comment", "Postgres", "06"), entry(5, "report", "pr_ready: Done", "09"), entry(6, "ci", "A check failed", "10")],
    [run("run_1", "01"), run("run_2", "07")],
  );
  assert.deepEqual(messages.map(message => message.role), ["user", "assistant", "user", "assistant", "system"]);
  assert.deepEqual(messages[1].content.map(part => part.type), ["data-run", "text", "text"]);
  assert.deepEqual(messages[1].content.slice(1).map(part => part.text), ["Stopped", "Which?"]);
  assert.equal(messages[0].metadata.custom.author, "alice");
});

test("a repeated summary shows once and a queued run comes last", () => {
  const messages = ticketThread([entry(1, "request", "Fix it", "00"), entry(2, "report", "failed: No tests", "05"), entry(3, "message", "No tests", "05")], [{ id: "run_1", status: "queued" }]);
  assert.deepEqual(messages[1].content.map(part => part.type === "text" ? part.text : part.type), ["No tests", "data-run"]);
});
