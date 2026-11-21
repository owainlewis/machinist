import assert from "node:assert/strict";
import test from "node:test";
import { latestAction, outputLimit, pipeline, pollEvents, stepTime, steps } from "./step-log.js";

const output = (text, stream = "stdout") => ({ type: "process.output", stream, encoding: "base64", data: Buffer.from(text).toString("base64") });
const started = name => ({ type: "step.started", message: `name=${name}`, time: "2026-11-17T21:24:57Z" });

test("each step keeps what it printed", () => {
  const events = [started("checkout"), output("Switched to a new branch\n", "stderr"), started("test"), output("ok  pkg\n"), output("PASS\n")];
  const stages = [{ name: "checkout", state: "succeeded", duration_millis: 5088 }, { name: "test", state: "failed", duration_millis: 900, error: "command exited with status 1" }, { name: "publish", state: "skipped" }];
  assert.deepEqual(steps(events, stages, "failed").map(({ name, output, state, error }) => ({ name, output, state, error })), [
    { name: "checkout", output: "Switched to a new branch\n", state: "succeeded", error: "" },
    { name: "test", output: "ok  pkg\nPASS\n", state: "failed", error: "command exited with status 1" },
    { name: "publish", output: "", state: "skipped", error: "" },
  ]);
});

test("a run from before the steps runner keeps its stage output", () => {
  const events = [{ type: "stage.started", message: "name=unit tests kind=script" }, output("ok\n"), { type: "stage.started", message: "name=agent kind=agent" }, output("{}")];
  const stages = [{ name: "unit tests", kind: "script", state: "succeeded" }, { name: "agent", kind: "agent", state: "succeeded" }];
  assert.deepEqual(steps(events, stages, "succeeded").map(step => [step.name, step.output]), [["unit tests", "ok\n"], ["agent", "{}"]]);
});

test("while a run is going its last step is the one running", () => {
  const result = steps([started("checkout"), started("plan"), output("thinking")], [], "running");
  assert.deepEqual(result.map(step => [step.name, step.state]), [["checkout", "succeeded"], ["plan", "running"]]);
});

test("a step keeps the end of long output", () => {
  const [step] = steps([started("test"), output("a".repeat(outputLimit)), output("the failure")], [], "running");
  assert.equal(step.output.length, outputLimit);
  assert.ok(step.output.endsWith("the failure"));
});

test("a multibyte character split across chunks decodes whole", () => {
  const bytes = Buffer.from("é");
  const events = [started("plan"), { type: "process.output", encoding: "base64", data: bytes.subarray(0, 1).toString("base64") }, { type: "process.output", encoding: "base64", data: bytes.subarray(1).toString("base64") }];
  assert.equal(steps(events, [], "running")[0].output, "é");
});

test("events stop being polled once a run has ended", () => {
  assert.equal(pollEvents({ status: "running" }), true);
  assert.equal(pollEvents({ status: "queued" }), true);
  assert.equal(pollEvents({ status: "succeeded", ended_at: "x" }), false);
  assert.equal(pollEvents({ status: "cancelled" }), true);
  assert.equal(pollEvents({ status: "cancelled", ended_at: "x" }), false);
});

test("the pipeline shows every preset step, and the ones not reached yet", async () => {
  const { pipeline } = await import("./step-log.js");
  const ran = [{ name: "checkout", state: "succeeded" }, { name: "plan", state: "running" }];
  assert.deepEqual(pipeline(["checkout", "plan", "test"], ran, "running").map(step => [step.name, step.state]), [["checkout", "succeeded"], ["plan", "running"], ["test", "pending"]]);
  assert.deepEqual(pipeline(["checkout", "plan", "test"], [], "").map(step => step.state), ["pending", "pending", "pending"]);
  assert.deepEqual(pipeline(["checkout", "test"], [{ name: "checkout", state: "failed" }], "failed").map(step => step.state), ["failed", "skipped"]);
  assert.deepEqual(pipeline(["new"], [{ name: "old", state: "succeeded" }], "succeeded").map(step => step.name), ["old"]);
});

test("the latest action is the last line agent-log marked as one", () => {
  assert.equal(latestAction("Claude Code started\n▸ Read go.mod\nThinking it over\n▸ Run go test ./...\nok\n"), "Run go test ./...");
  assert.equal(latestAction("plain output\n"), "");
  assert.equal(latestAction(), "");
});

test("a running step counts up from when it started, and an ended one shows how long it took", () => {
  const start = Date.parse("2026-11-19T15:00:00Z");
  assert.equal(stepTime({ state: "running", started_at: "2026-11-19T15:00:00Z" }, start + 192000), "3m 12s");
  assert.equal(stepTime({ state: "succeeded", started_at: "2026-11-19T15:00:00Z", duration_millis: 45000 }, start + 999999), "45s");
  assert.equal(stepTime({ state: "pending" }), "");
});

test("pipeline names each step's agents, from the run once it ran and the preset before", () => {
  const review = { name: "review", harness: "agy", model: "gemini-4-neon-high" };
  const implement = { name: "implement", harness: "claude" };
  const preset = [{ name: "checkout", agents: [] }, { name: "implement", agents: [implement] }, { name: "review", agents: [review] }];
  const ran = [{ name: "checkout", state: "succeeded", agents: [] }, { name: "implement", state: "succeeded", agents: [{ ...implement, model: "opus" }] }];
  assert.deepEqual(pipeline(preset, ran, "running").map(step => step.agents), [[], [{ ...implement, model: "opus" }], [review]]);
  assert.deepEqual(steps([], [{ name: "review", state: "succeeded", agents: [review] }], "succeeded")[0].agents, [review]);
  // A step that ran and called no agent, such as one that skipped a branch, shows none.
  assert.deepEqual(pipeline(preset, [{ name: "checkout", state: "succeeded" }, { name: "implement", state: "succeeded", agents: [] }, { name: "review", state: "failed", agents: [] }], "failed").map(step => step.agents), [[], [], []]);
});
