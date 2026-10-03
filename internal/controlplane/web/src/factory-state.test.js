import test from "node:test";
import assert from "node:assert/strict";
import { groupTasks, displayEvents, isBusy } from "./factory-state.js";
test("completion does not imply delivery", () => {
  const g = groupTasks([
    { id: "a", stage: "Review", status: "completed" },
    { id: "b", stage: "Done" },
  ]);
  assert.equal(g.Review[0].id, "a");
  assert.equal(g.Done[0].id, "b");
});
test("chunks combine within a turn", () => {
  const e = displayEvents([
    { kind: "message_delta", turn_id: "a", text: "Hi " },
    { kind: "message_delta", turn_id: "a", text: "you" },
    { kind: "message_delta", turn_id: "b", text: "Next" },
  ]);
  assert.equal(e.length, 2);
  assert.equal(e[0].text, "Hi you");
});
test("permission waiting still allows cancellation", () =>
  assert.equal(isBusy({ status: "awaiting_permission" }), true));

test("activity updates avoid empty and repeated entries while retaining errors", () => {
  const events = displayEvents([
    { kind: "activity" },
    { kind: "activity", title: "Read files", status: "running" },
    { kind: "activity", title: "Read files", status: "running" },
    { kind: "activity", title: "Read files", status: "completed" },
    { kind: "error", text: "Read failed" },
    { kind: "error", text: "Read failed" },
  ]);
  assert.equal(events.length, 4);
  assert.equal(events[1].status, "completed");
  assert.equal(events[2].text, "Read failed");
  assert.equal(events[3].kind, "error");
});
