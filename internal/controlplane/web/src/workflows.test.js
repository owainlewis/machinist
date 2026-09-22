import test from "node:test";
import assert from "node:assert/strict";
import { boardColumnForState, currentRun, groupJobsByBoardColumn } from "./runs-board.js";

test("workflow approval, blockers, and interruptions remain visible as attention", () => {
 for (const state of ["awaiting_approval", "blocked", "interrupted"]) {
  assert.equal(boardColumnForState(state), "attention");
  const grouped = groupJobsByBoardColumn([{id: "job", state}]);
  assert.equal(grouped.attention.length, 1);
  assert.equal(grouped.finished.length, 0);
 }
});
test("a queued next workflow step replaces the previous completed step in the summary", () => {
 const runs = [{id: "triage", state: "succeeded"}, {id: "build", state: "queued"}];
 assert.equal(currentRun({workflow: {name: "deliver"}, runs}).id, "build");
});
