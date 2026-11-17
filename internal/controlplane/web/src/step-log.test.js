import assert from "node:assert/strict";
import test from "node:test";
import { outputLimit, pollEvents, steps } from "./step-log.js";

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
