import test from "node:test";
import assert from "node:assert/strict";
import { agentSummary, agentTitle, harnessLabel, presetAgents } from "./agents.js";

test("an agent is summed up by its model, or its harness's default", () => {
  assert.equal(agentSummary({ name: "review", harness: "agy", model: "gemini-4-neon-high" }), "gemini-4-neon-high");
  assert.equal(agentSummary({ name: "implement", harness: "claude" }), "Claude Code default");
  assert.equal(harnessLabel("mystery"), "mystery");
  assert.equal(agentTitle({ name: "review", harness: "codex" }), "Agent review, on Codex, model the harness default");
});

test("a preset's agents are listed once each, in the order steps run them", () => {
  const review = { name: "review", harness: "agy", model: "gemini-4-neon-high" };
  const revise = { name: "revise", harness: "claude" };
  const preset = { steps: [{ name: "checkout", agents: [] }, { name: "review", agents: [review] }, { name: "revise", agents: [revise, review] }] };
  assert.deepEqual(presetAgents(preset), [review, revise]);
  assert.deepEqual(presetAgents(undefined), []);
});
