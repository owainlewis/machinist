// Pure formatting and request helpers for the Jobs → Automations view.

export const automationExample = `---
schedule: "0 9 * * 1-5"
timezone: UTC
repo: owner/name
preset: default
---
Review open pull requests and report any that are ready to merge.`;

// formatNextDue describes a future run time relative to now.
export function formatNextDue(value, now = Date.now()) {
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return "—";
  const diffSeconds = Math.floor((time - now) / 1000);
  if (diffSeconds <= 0) return "Due now";
  if (diffSeconds < 60) return "in <1m";
  const minutes = Math.floor(diffSeconds / 60);
  if (minutes < 60) return `in ${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remMinutes = minutes % 60;
  if (hours < 24) return remMinutes ? `in ${hours}h ${remMinutes}m` : `in ${hours}h`;
  const days = Math.floor(hours / 24);
  const remHours = hours % 24;
  if (days < 7 && remHours) return `in ${days}d ${remHours}h`;
  return `in ${days}d`;
}

// formatDueAt formats an RFC 3339 timestamp in the automation's timezone for
// tooltips and secondary display. Falls back cleanly if the timezone is unknown.
export function formatDueAt(value, timeZone) {
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return "";
  const date = new Date(time);
  const options = { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false, timeZoneName: "short" };
  try {
    return new Intl.DateTimeFormat("en-US", timeZone ? { ...options, timeZone } : options).format(date);
  } catch {
    return date.toISOString().replace("T", " ").slice(0, 16) + " UTC";
  }
}

export function isPromptEdited(current, saved) {
  return String(current ?? "").trim() !== String(saved ?? "").trim();
}

export function canRunAutomation(prompt) {
  return String(prompt ?? "").trim().length > 0;
}

// runAutomationBody returns the optional JSON body for POST /api/v1/automations/{name}/run.
// Leaving it undefined when unchanged runs the saved prompt as-is.
export function runAutomationBody(current, saved) {
  const trimmed = String(current ?? "").trim();
  if (!isPromptEdited(trimmed, saved)) return undefined;
  return { prompt: trimmed };
}

export function automationRunPath(name) {
  return `/api/v1/automations/${encodeURIComponent(name)}/run`;
}
