import assert from "node:assert/strict";
import test from "node:test";
import {
  automationExample, automationPath, automationPayload, automationRunPath,
  canRunAutomation, canSaveAutomation, formatDueAt, formatNextDue,
  isPromptEdited, isRunEdited, onlineHarnessSet, runAutomationBody,
} from "./automations.js";

test("formatNextDue describes future run times cleanly", () => {
  const now = Date.parse("2026-11-25T12:00:00Z");
  assert.equal(formatNextDue("", now), "—");
  assert.equal(formatNextDue("not-a-date", now), "—");
  assert.equal(formatNextDue("2026-11-25T11:59:00Z", now), "Due now");
  assert.equal(formatNextDue("2026-11-25T12:00:00Z", now), "Due now");
  assert.equal(formatNextDue("2026-11-25T12:00:30Z", now), "in <1m");
  assert.equal(formatNextDue("2026-11-25T12:45:00Z", now), "in 45m");
  assert.equal(formatNextDue("2026-11-25T14:00:00Z", now), "in 2h");
  assert.equal(formatNextDue("2026-11-25T14:15:00Z", now), "in 2h 15m");
  assert.equal(formatNextDue("2026-11-27T15:00:00Z", now), "in 2d 3h");
  assert.equal(formatNextDue("2026-12-05T15:00:00Z", now), "in 10d");
});

test("formatDueAt formats in the given timezone and falls back safely", () => {
  assert.equal(formatDueAt("invalid", "UTC"), "");
  assert.match(formatDueAt("2026-11-26T09:00:00Z", "UTC"), /Nov 26.*09:00/);
  assert.match(formatDueAt("2026-11-26T09:00:00Z", "Not/A_Zone"), /2026-11-26 09:00 UTC/);
});

test("runAutomationBody sends overrides only for changed fields", () => {
  assert.equal(isPromptEdited("Check PRs", "Check PRs"), false);
  assert.equal(isPromptEdited("  Check PRs  ", "Check PRs"), false);
  assert.equal(isPromptEdited("Check only flaky tests", "Check PRs"), true);

  assert.equal(canRunAutomation(""), false);
  assert.equal(canRunAutomation("   \n  "), false);
  assert.equal(canRunAutomation("Check PRs"), true);

  assert.equal(runAutomationBody("Check PRs", "Check PRs"), undefined);
  assert.equal(runAutomationBody("  Check PRs\n", "Check PRs"), undefined);
  assert.deepEqual(runAutomationBody("  Custom prompt  ", "Check PRs"), { prompt: "Custom prompt" });

  const saved = { repo: "owner/app", agent: "default", harness: "claude", model: "opus", prompt: "Check PRs" };
  assert.equal(isRunEdited({ repo: "owner/app", preset: "default", harness: "claude", model: "opus", prompt: "Check PRs" }, saved), false);
  assert.equal(runAutomationBody({ repo: "owner/app", preset: "default", harness: "claude", model: "opus", prompt: "Check PRs" }, saved), undefined);
  assert.deepEqual(
    runAutomationBody({ repo: "owner/api", preset: "neon", harness: "codex", model: "gpt-5-codex", prompt: "Check PRs" }, saved),
    { repo: "owner/api", preset: "neon", harness: "codex", model: "gpt-5-codex" },
  );
  assert.deepEqual(
    runAutomationBody({ repo: "owner/app", preset: "default", harness: "", model: "", prompt: "Check PRs" }, saved),
    { harness: "", model: "" },
  );

  assert.equal(automationPath("daily-review"), "/api/v1/automations/daily-review");
  assert.equal(automationRunPath("daily-review"), "/api/v1/automations/daily-review/run");
  assert.match(automationExample, /schedule:.*\ntimezone:.*\nrepo:.*\npreset:/);
});

test("automationPayload and canSaveAutomation handle manual and scheduled forms", () => {
  assert.equal(canSaveAutomation({ name: "", repo: "o/r", preset: "default", prompt: "Go" }), false);
  assert.equal(canSaveAutomation({ name: "", repo: "o/r", preset: "default", prompt: "Go" }, { requireName: false }), true);
  assert.equal(canSaveAutomation({ name: "audit", repo: "o/r", preset: "default", prompt: "Go" }), true);

  assert.deepEqual(
    automationPayload({ name: " audit ", repo: "owner/app", preset: "default", harness: "codex", model: "gpt-5-codex", schedule: "", timezone: "UTC", prompt: " Audit " }),
    { name: "audit", repo: "owner/app", preset: "default", harness: "codex", model: "gpt-5-codex", schedule: "", timezone: "", prompt: "Audit" },
  );
  assert.deepEqual(
    automationPayload({ name: "audit", repo: "owner/app", preset: "default", schedule: "0 9 * * *", timezone: "", prompt: "Audit" }, { includeName: false }),
    { repo: "owner/app", preset: "default", harness: "", model: "", schedule: "0 9 * * *", timezone: "UTC", prompt: "Audit" },
  );
  assert.deepEqual(
    automationPayload({ name: "audit", repo: "owner/app", preset: "default", schedule: "", timezone: "", prompt: "Audit", runs_on: [" gpu ", "vpc", ""] }, { includeName: false }),
    { repo: "owner/app", preset: "default", harness: "", model: "", schedule: "", timezone: "", prompt: "Audit", runs_on: ["gpu", "vpc"] },
  );
  assert.deepEqual([...onlineHarnessSet([{ harnesses: ["steps", "claude", "codex"] }])], ["claude", "codex"]);
});

test("automation helpers validate, serialize, and override preset vars", () => {
  const declared = [
    { name: "environment", description: "Env", default: undefined, required: true },
    { name: "region", description: "Region", default: "us-east-1", required: false },
  ];
  assert.equal(canSaveAutomation({ name: "deploy", repo: "o/r", preset: "deploy", prompt: "Go", vars: { environment: "", region: "us-east-1" } }, { presetInputs: declared }), false);
  assert.equal(canSaveAutomation({ name: "deploy", repo: "o/r", preset: "deploy", prompt: "Go", vars: { environment: "staging", region: "us-east-1" } }, { presetInputs: declared }), true);
  assert.deepEqual(
    automationPayload({ name: "deploy", repo: "o/r", preset: "deploy", prompt: "Go", vars: { environment: "staging" } }, { presetInputs: declared }),
    { name: "deploy", repo: "o/r", preset: "deploy", prompt: "Go", harness: "", model: "", schedule: "", timezone: "", vars: { environment: "staging", region: "us-east-1" } },
  );

  const saved = { repo: "o/r", agent: "deploy", harness: "", model: "", prompt: "Go", vars: { environment: "staging", region: "us-east-1" } };
  assert.equal(isRunEdited({ repo: "o/r", preset: "deploy", harness: "", model: "", prompt: "Go", vars: { environment: "staging", region: "us-east-1" } }, saved, declared), false);
  assert.equal(runAutomationBody({ repo: "o/r", preset: "deploy", harness: "", model: "", prompt: "Go", vars: { environment: "staging", region: "us-east-1" } }, saved, declared), undefined);
  assert.equal(isRunEdited({ repo: "o/r", preset: "deploy", harness: "", model: "", prompt: "Go", vars: { environment: "prod", region: "us-east-1" } }, saved, declared), true);
  assert.deepEqual(
    runAutomationBody({ repo: "o/r", preset: "deploy", harness: "", model: "", prompt: "Go", vars: { environment: "prod", region: "us-east-1" } }, saved, declared),
    { vars: { environment: "prod", region: "us-east-1" } },
  );
  assert.equal(canRunAutomation("Go", { environment: "" }, declared), false);
  assert.equal(canRunAutomation("Go", { environment: "prod" }, declared), true);
});
