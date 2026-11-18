import assert from "node:assert/strict";
import test from "node:test";
import { canStart, initialPreset, newJobKey, offersPresetChoice, runRequest, titleOf } from "./new-job.js";

test("a new job is a run API request with a key of its own", () => {
  assert.equal(newJobKey(() => "abc"), "ui-abc");
  assert.notEqual(newJobKey(), newJobKey());
  assert.match(newJobKey(), /^ui-[0-9a-f]{32}$/);
  assert.deepEqual(runRequest({ key: "ui-1", preset: "coding", repo: "owner/repo", prompt: "  Fix it \n" }), { key: "ui-1", preset: "coding", vars: { repo: "owner/repo" }, prompt: "Fix it" });
});

test("a job needs a preset, a repository and a prompt", () => {
  assert.equal(canStart({ preset: "coding", repo: "owner/repo", prompt: "Fix it" }), true);
  for (const missing of [{ preset: "" }, { repo: "" }, { prompt: "   \n" }]) {
    assert.equal(canStart({ preset: "coding", repo: "owner/repo", prompt: "Fix it", ...missing }), false);
  }
});

test("the default preset is chosen first when it exists", () => {
  const presets = [{ name: "coding" }, { name: "spec" }];
  assert.equal(initialPreset(presets, "spec"), "spec");
  assert.equal(initialPreset(presets, "ghost"), "coding");
  assert.equal(initialPreset([], "coding"), "");
});

test("the title is the prompt's first line, cut like the server cuts it", () => {
  assert.equal(titleOf("\n  Fix the toasts  \nMore detail"), "Fix the toasts");
  assert.equal(titleOf("x".repeat(80)), "x".repeat(80));
  assert.equal(titleOf("é".repeat(90)), `${"é".repeat(79)}…`);
  assert.equal(titleOf(""), "");
});

test("the form asks for a preset only when there is more than one", () => {
  assert.equal(offersPresetChoice([{ name: "default" }]), false);
  assert.equal(offersPresetChoice([{ name: "default" }, { name: "review" }]), true);
});
