import { harnessLabel } from "./agents.js";
import { automationHarnesses } from "./automations.js";

export const supportedHarnesses = automationHarnesses;

const modelPlaceholders = {
  agy: "Harness default (e.g. gemini-4-neon-high)",
  claude: "Harness default (e.g. sonnet, opus, haiku)",
  codex: "Harness default (e.g. gpt-5-codex, gpt-5)",
  pi: "Harness default (e.g. claude-sonnet-4-5)",
};

export function harnessOptionLabel(name, onlineHarnesses = new Set()) {
  const label = harnessLabel(name);
  if (onlineHarnesses.size > 0 && !onlineHarnesses.has(name)) return `${label} (no online runner)`;
  return label;
}

export function modelPlaceholder(harness = "") {
  return modelPlaceholders[String(harness ?? "").trim()] || "Preset / harness default";
}

// randomHex uses getRandomValues, which works on plain http too. randomUUID
// needs a secure context, and a control plane on a LAN address is not one.
function randomHex() {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
}

// The run API takes a key so a retried request starts one job, not two.
export function newJobKey(random = randomHex) {
  return `ui-${random()}`;
}

function normalizedKeyFields(body) {
  if (!body || typeof body !== "object") return body;
  const normalized = {};
  for (const [name, value] of Object.entries(body)) {
    if (name === "harness" || name === "model") {
      const trimmed = String(value ?? "").trim();
      if (trimmed) normalized[name] = trimmed;
      continue;
    }
    normalized[name] = value;
  }
  return normalized;
}

// keyFor keeps one key for one request. A retry of the same fields reuses the
// last key, so a lost response cannot start a second job. Edited fields get a
// new key, because the run API would answer an old key with the old job.
export function keyFor(last, body, make = newJobKey) {
  const fields = JSON.stringify(normalizedKeyFields(body));
  if (last && last.fields === fields) return last;
  return { key: make(), fields };
}

// runRequest is the body of POST /machinist/run for what the form holds.
export function runRequest({ key, preset, repo, prompt, harness = "", model = "" }) {
  const body = { key, preset, vars: { repo }, prompt: prompt.trim() };
  const trimmedHarness = String(harness ?? "").trim();
  const trimmedModel = String(model ?? "").trim();
  if (trimmedHarness) body.harness = trimmedHarness;
  if (trimmedModel) body.model = trimmedModel;
  return body;
}

// The form can start a job once it has a preset, a repository and a prompt.
export function canStart({ preset, repo, prompt }) {
  return Boolean(preset && repo && prompt.trim());
}

// The preset to show first: the configured default, or else the first.
export function initialPreset(presets, preferred) {
  if (presets.some(preset => preset.name === preferred)) return preferred;
  return presets[0]?.name || "";
}

// The first line of the prompt becomes the job's title, as the server does it.
export function titleOf(prompt, limit = 80) {
  const line = prompt.trim().split("\n")[0].trim();
  const characters = Array.from(line);
  return characters.length <= limit ? line : `${characters.slice(0, limit - 1).join("").trim()}…`;
}
