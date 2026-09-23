import assert from "node:assert/strict";
import test from "node:test";
import { firstSelection, selectionChoices } from "./selection.js";

test("workflows and commands are both offered, workflows first", () => {
  assert.deepEqual(selectionChoices({ workflows: ["deliver"], commands: ["audit", "run"] }).map(c => c.value),
    ["workflow:deliver", "command:audit", "command:run"]);
});

test("run is the default choice when configured", () => {
  assert.equal(firstSelection({ workflows: ["deliver"], commands: ["audit", "run"] }), "command:run");
  assert.equal(firstSelection({ workflows: ["deliver", "run"], commands: ["run"] }), "workflow:run");
});

test("the first choice is the default without run", () => {
  assert.equal(firstSelection({ workflows: [], commands: ["audit"] }), "command:audit");
  assert.equal(firstSelection({}), "");
});
