import assert from "node:assert/strict";
import test from "node:test";
import { agentFrontMatter, agentOverridden, checkLink, dayLabel, durationShort, factoryUpdateLine, githubLine, githubWarnings, groupAgents, pollLabel, roleLabel, settingsPages, stepKind, stepKindLabel, stepShellPreview, tokenLine, tokenPrefixLabel, validateTokenName } from "./settings.js";

test("the side nav lists one page per topic, overview first", () => {
  assert.deepEqual(settingsPages.map(([page]) => page), ["overview", "projects", "github", "linear", "tokens", "presets"]);
});

test("a setup check links to the page that holds more about it", () => {
  assert.deepEqual(checkLink("GitHub"), ["#/settings/github", "Open GitHub settings"]);
  assert.deepEqual(checkLink("Projects"), ["#/settings/projects", "Open Projects settings"]);
  assert.deepEqual(checkLink("Agents"), ["#/settings/presets", "Open Agents settings"]);
  assert.deepEqual(checkLink("Factory"), ["#/settings/presets", "Open Presets and agents"]);
  assert.deepEqual(checkLink("Runner"), ["#/runners", "Open Runners"], "runners have a page of their own");
  assert.deepEqual(checkLink("Unknown"), ["", ""]);
});

test("the poll interval reads in minutes and seconds", () => {
  assert.equal(pollLabel(60), "Every 1m");
  assert.equal(pollLabel(90), "Every 1m 30s");
  assert.equal(pollLabel(15), "Every 15s");
  assert.equal(pollLabel(0), "");
});

test("roles and token counts read as words", () => {
  assert.equal(roleLabel("operator")[0], "Person");
  assert.equal(roleLabel("runner")[0], "Runner");
  assert.equal(roleLabel("all")[0], "Person and runner");
  assert.equal(tokenLine([{}, {}]), "2 active tokens.");
  assert.equal(tokenLine([{}, { revoked_at: "2026-11-20T00:00:00Z" }]), "1 active token, 1 revoked.");
  assert.equal(tokenPrefixLabel("mch_ab12"), "mch_ab12…");
  assert.equal(tokenPrefixLabel(""), "No prefix");
  assert.equal(tokenPrefixLabel(undefined), "No prefix");
  assert.deepEqual(validateTokenName("  owain  "), { name: "owain", error: "" });
  assert.deepEqual(validateTokenName("   "), { name: "", error: "" });
  assert.deepEqual(validateTokenName("a/b"), { name: "a/b", error: "Token names cannot contain /." });
  assert.equal(dayLabel("2026-11-20T12:00:00Z"), "Nov 20, 2026");
  assert.equal(dayLabel("nope"), "");
});

test("a repository with a warning or a problem counts once", () => {
  assert.equal(githubWarnings({ repositories: [{ warnings: [] }, { warnings: ["a", "b"] }, { warnings: [], problem: "x" }] }), 2);
  assert.equal(githubWarnings(null), 0);
});

test("the GitHub page says how runs reach GitHub", () => {
  assert.equal(githubLine({ repositories: [] }), "Runs use each runner's own GH_TOKEN.");
  assert.equal(githubLine({ app: { id: 1 }, repositories: [{ warnings: ["x"] }] }), "Runs use short-lived tokens from the GitHub App. 1 repository needs a look.");
});

test("factory updates read as a sentence", () => {
  assert.equal(factoryUpdateLine([]), "");
  assert.equal(factoryUpdateLine([{ path: "scripts/publish.sh", status: "outdated" }]), "1 factory file is out of date.");
  assert.equal(factoryUpdateLine([{ path: "a", status: "outdated" }, { path: "b", status: "missing" }]), "2 factory files are out of date.");
});

test("preset steps distinguish direct agent, script+agent, and shell steps", () => {
  const shell = { name: "checkout", run: "checkout.sh\n# comment", agents: [] };
  const agent = { name: "implement", run: "machinist agent implement", agents: [{ name: "implement", harness: "claude" }] };
  const scriptAgent = { name: "review", run: "review.sh", scripts: [{ path: "scripts/review.sh", content: "machinist agent review" }], agents: [{ name: "review", harness: "claude" }] };
  assert.equal(stepKind(shell), "shell");
  assert.equal(stepKindLabel(shell), "Shell");
  assert.equal(stepShellPreview(shell), "checkout.sh");
  assert.equal(stepKind(agent), "agent");
  assert.equal(stepKindLabel(agent), "Agent");
  assert.equal(stepShellPreview(agent), "");
  assert.equal(stepKind(scriptAgent), "script_agent");
  assert.equal(stepKindLabel(scriptAgent), "Script · Agent");
  assert.equal(stepShellPreview(scriptAgent), "");
});

test("durations, front matter, and override detection format cleanly", () => {
  assert.equal(durationShort(0), "");
  assert.equal(durationShort(45000), "45s");
  assert.equal(durationShort(5 * 60 * 1000), "5m");
  assert.equal(durationShort(90 * 60 * 1000), "1h 30m");
  assert.equal(durationShort(60 * 60 * 1000), "1h");
  assert.equal(agentFrontMatter({ harness: "codex", model: "gpt-5", command: "/opt/codex" }), "harness: codex\nmodel: gpt-5\ncommand: /opt/codex");
  assert.equal(agentFrontMatter({ harness: "claude" }), "harness: claude");
  assert.equal(agentOverridden({ harness: "claude", model: "" }, { harness: "claude", model: "" }), false);
  assert.equal(agentOverridden({ harness: "agy", model: "gemini-4-neon-high" }, { harness: "claude", model: "" }), true);
  assert.equal(agentOverridden({ harness: "claude", model: "opus" }, { harness: "claude", model: "sonnet" }), true);
});

test("groupAgents separates reusable roles, collapses identical prompts, groups preset-specific agents, and isolates unused agents", () => {
  // Single preset keeps active agents flat and separates unused ones.
  const single = groupAgents([
    { name: "implement", harness: "claude", prompt: "Implement.", presets: ["default"] },
    { name: "unused", harness: "codex", prompt: "Unused.", presets: [] },
  ]);
  assert.equal(single.hasShared, false);
  assert.deepEqual(single.primary.map(group => group.key), ["implement"]);
  assert.deepEqual(single.presetGroups, []);
  assert.deepEqual(single.unused.map(group => group.key), ["unused"]);

  // Shared roles, duplicate-prompt variants, preset-specific agents, and unused agents.
  const mixed = groupAgents([
    { name: "implement", harness: "claude", prompt: "Implement the task.", presets: ["default"] },
    { name: "implement-neon", harness: "agy", model: "gemini-4-neon-high", prompt: "  Implement the task.\n", presets: ["neon"] },
    { name: "review", harness: "claude", prompt: "Review the diff.", presets: ["default", "neon"] },
    { name: "writer", harness: "claude", prompt: "Write a spec.", presets: ["spec"] },
    { name: "scratch", harness: "pi", prompt: "Old prompt.", presets: [] },
  ]);
  assert.equal(mixed.hasShared, true);
  assert.deepEqual(mixed.primary.map(group => group.key), ["implement", "review"]);
  assert.deepEqual(mixed.primary[0].variants.map(v => v.name), ["implement", "implement-neon"]);
  assert.deepEqual(mixed.primary[0].presets, ["default", "neon"]);
  assert.deepEqual(mixed.primary[0].harnesses, ["claude", "agy"]);
  assert.equal(mixed.presetGroups.length, 1);
  assert.equal(mixed.presetGroups[0].preset, "spec");
  assert.deepEqual(mixed.presetGroups[0].groups.map(group => group.key), ["writer"]);
  assert.deepEqual(mixed.unused.map(group => group.key), ["scratch"]);

  // Multiple presets with completely disjoint agents group by preset.
  const disjoint = groupAgents([
    { name: "coder", harness: "claude", prompt: "Code.", presets: ["coding"] },
    { name: "writer", harness: "agy", prompt: "Write.", presets: ["spec"] },
  ]);
  assert.equal(disjoint.hasShared, false);
  assert.deepEqual(disjoint.primary, []);
  assert.deepEqual(disjoint.presetGroups.map(entry => entry.preset), ["coding", "spec"]);
});
