import assert from "node:assert/strict";
import test from "node:test";
import { displayTriggerValue, triggerFields, triggerHealthTone, triggerRows, triggerView } from "./trigger-state.js";

test("trigger rows expose every generic status field", () => {
  const trigger = {
    next_due: "2026-08-27T16:00:00Z",
    last_attempt: "2026-08-27T15:00:00Z",
    last_success: "2026-08-27T15:01:00Z",
    active_job: "job_123",
    candidate_count: 9,
    admission_count: 4,
    coalesced_count: 2,
  };

  assert.deepEqual(triggerRows(trigger), [
    { field: "next_due", label: "Next due", value: "2026-08-27T16:00:00Z" },
    { field: "last_attempt", label: "Last attempt", value: "2026-08-27T15:00:00Z" },
    { field: "last_success", label: "Last success", value: "2026-08-27T15:01:00Z" },
    { field: "active_job", label: "Active job", value: "job_123" },
    { field: "candidate_count", label: "Candidates", value: "9" },
    { field: "admission_count", label: "Admissions", value: "4" },
    { field: "coalesced_count", label: "Coalesced", value: "2" },
  ]);
  assert.deepEqual(triggerFields.map(([field]) => field), Object.keys(trigger));
});

test("missing trigger values have explicit empty states", () => {
  assert.equal(displayTriggerValue("next_due", "0001-01-01T00:00:00Z"), "Not yet");
  assert.equal(displayTriggerValue("last_attempt", ""), "Not yet");
  assert.equal(displayTriggerValue("active_job", ""), "None");
  assert.equal(displayTriggerValue("candidate_count", undefined), "0");
});

test("trigger health maps healthy, stale, and failed states to distinct tones", () => {
  assert.equal(triggerHealthTone("healthy"), "success");
  assert.equal(triggerHealthTone("stale"), "warning");
  assert.equal(triggerHealthTone("failed"), "danger");
  assert.equal(triggerHealthTone("unknown"), "neutral");
});

test("trigger view includes identity, family, health, and the latest error", () => {
  const view = triggerView({
    identity: "cron/nightly-audit",
    family: "cron",
    health: "failed",
    error: "worker admission failed",
  });

  assert.equal(view.identity, "cron/nightly-audit");
  assert.equal(view.family, "cron");
  assert.equal(view.health, "failed");
  assert.equal(view.error, "worker admission failed");
  assert.equal(view.rows.length, triggerFields.length);
});
