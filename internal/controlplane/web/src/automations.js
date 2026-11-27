// Pure formatting and request helpers for the Automations page.

export const automationHarnesses = ["agy", "claude", "codex", "pi"];

export const automationExample = `---
schedule: "0 9 * * 1-5"   # optional; omit for manual-only
timezone: UTC
repo: owner/name
preset: default
harness: codex            # optional; agy, claude, codex or pi
model: gpt-5-codex        # optional
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

export function isRunEdited(current, saved) {
  if (!current || typeof current !== "object") return isPromptEdited(current, saved);
  return (
    String(current.prompt ?? "").trim() !== String(saved?.prompt ?? "").trim() ||
    String(current.repo ?? "").trim() !== String(saved?.repo ?? "").trim() ||
    String(current.preset ?? "").trim() !== String(saved?.agent ?? saved?.preset ?? "").trim() ||
    String(current.harness ?? "").trim() !== String(saved?.harness ?? "").trim() ||
    String(current.model ?? "").trim() !== String(saved?.model ?? "").trim()
  );
}

export function canRunAutomation(prompt) {
  return String(prompt ?? "").trim().length > 0;
}

export function canSaveAutomation(form, { requireName = true } = {}) {
  const name = String(form?.name ?? "").trim();
  const repo = String(form?.repo ?? "").trim();
  const preset = String(form?.preset ?? "").trim();
  const prompt = String(form?.prompt ?? "").trim();
  if (requireName && !name) return false;
  return Boolean(repo && preset && prompt);
}

export function automationPayload(form, { includeName = true } = {}) {
  const schedule = String(form?.schedule ?? "").trim();
  const timezone = schedule ? (String(form?.timezone ?? "").trim() || "UTC") : "";
  const body = {
    repo: String(form?.repo ?? "").trim(),
    preset: String(form?.preset ?? "").trim(),
    prompt: String(form?.prompt ?? "").trim(),
    harness: String(form?.harness ?? "").trim(),
    model: String(form?.model ?? "").trim(),
    schedule,
    timezone,
  };
  if (includeName) body.name = String(form?.name ?? "").trim();
  return body;
}

// runAutomationBody returns the optional JSON body for POST /api/v1/automations/{name}/run.
// Leaving it undefined when unchanged runs the saved automation as-is.
export function runAutomationBody(current, saved) {
  if (typeof current === "string" || current == null) {
    const trimmed = String(current ?? "").trim();
    if (!isPromptEdited(trimmed, saved)) return undefined;
    return { prompt: trimmed };
  }
  const body = {};
  const prompt = String(current.prompt ?? "").trim();
  if (prompt !== String(saved?.prompt ?? "").trim()) body.prompt = prompt;
  const repo = String(current.repo ?? "").trim();
  if (repo && repo !== String(saved?.repo ?? "").trim()) body.repo = repo;
  const preset = String(current.preset ?? "").trim();
  const savedPreset = String(saved?.agent ?? saved?.preset ?? "").trim();
  if (preset && preset !== savedPreset) body.preset = preset;
  const harness = String(current.harness ?? "").trim();
  if (harness !== String(saved?.harness ?? "").trim()) body.harness = harness;
  const model = String(current.model ?? "").trim();
  if (model !== String(saved?.model ?? "").trim()) body.model = model;
  return Object.keys(body).length > 0 ? body : undefined;
}

export function automationPath(name) {
  return `/api/v1/automations/${encodeURIComponent(name)}`;
}

export function automationRunPath(name) {
  return `${automationPath(name)}/run`;
}

export function onlineHarnessSet(runners = []) {
  const set = new Set();
  for (const runner of runners || []) {
    for (const name of runner.harnesses || []) {
      if (name !== "steps") set.add(name);
    }
  }
  return set;
}
