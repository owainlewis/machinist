import assert from "node:assert/strict";
import test from "node:test";
import {
  automationExample, automationRunPath, canRunAutomation, formatDueAt, formatNextDue,
  isPromptEdited, runAutomationBody,
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

test("runAutomationBody sends an override only when the prompt changed", () => {
  assert.equal(isPromptEdited("Check PRs", "Check PRs"), false);
  assert.equal(isPromptEdited("  Check PRs  ", "Check PRs"), false);
  assert.equal(isPromptEdited("Check only flaky tests", "Check PRs"), true);

  assert.equal(canRunAutomation(""), false);
  assert.equal(canRunAutomation("   \n  "), false);
  assert.equal(canRunAutomation("Check PRs"), true);

  assert.equal(runAutomationBody("Check PRs", "Check PRs"), undefined);
  assert.equal(runAutomationBody("  Check PRs\n", "Check PRs"), undefined);
  assert.deepEqual(runAutomationBody("  Custom prompt  ", "Check PRs"), { prompt: "Custom prompt" });
  assert.equal(automationRunPath("daily-review"), "/api/v1/automations/daily-review/run");
  assert.match(automationExample, /schedule:.*\ntimezone:.*\nrepo:.*\npreset:/);
});
