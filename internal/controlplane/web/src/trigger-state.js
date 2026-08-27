const zeroTime = "0001-01-01T00:00:00Z";

export const triggerFields = [
  ["next_due", "Next due"],
  ["last_attempt", "Last attempt"],
  ["last_success", "Last success"],
  ["active_job", "Active job"],
  ["candidate_count", "Candidates"],
  ["admission_count", "Admissions"],
  ["coalesced_count", "Coalesced"],
];

export function triggerRows(trigger) {
  return triggerFields.map(([field, label]) => ({
    field,
    label,
    value: displayTriggerValue(field, trigger?.[field]),
  }));
}

export function triggerView(trigger) {
  return {
    identity: trigger?.identity || "unknown",
    family: trigger?.family || "unknown",
    health: trigger?.health || "unknown",
    error: trigger?.error || "",
    rows: triggerRows(trigger),
  };
}

export function displayTriggerValue(field, value) {
  if (field.endsWith("_count")) return Number.isFinite(value) ? String(value) : "0";
  if (field === "active_job") return value || "None";
  if (!value || value === zeroTime) return "Not yet";
  return value;
}

export function triggerHealthTone(health) {
  if (health === "healthy") return "success";
  if (health === "failed") return "danger";
  if (health === "stale") return "warning";
  return "neutral";
}
