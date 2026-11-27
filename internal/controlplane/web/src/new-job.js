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

// presetInputs returns a preset's declared inputs in alphabetical order.
export function presetInputs(preset) {
  const declared = preset?.inputs;
  if (!declared || typeof declared !== "object") return [];
  return Object.entries(declared)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, spec]) => ({
      name,
      description: spec?.description || "",
      default: spec?.default,
      required: spec?.default === undefined || spec?.default === null,
    }));
}

// initialInputValues builds the initial input map for a preset, applying saved
// overrides when present and falling back to each declared default.
export function initialInputValues(preset, saved = null) {
  const values = {};
  for (const item of presetInputs(preset)) {
    if (saved && Object.prototype.hasOwnProperty.call(saved, item.name) && saved[item.name] !== undefined && saved[item.name] !== null) {
      values[item.name] = String(saved[item.name]);
    } else {
      values[item.name] = item.default ?? "";
    }
  }
  return values;
}

// runRequest is the body of POST /machinist/run for what the form holds.
export function runRequest({ key, preset, repo, prompt, inputs, presetInputs: declared, harness = "", model = "" }) {
  const vars = { repo };
  if (Array.isArray(declared) && declared.length > 0) {
    for (const item of declared) {
      const hasValue = inputs && Object.prototype.hasOwnProperty.call(inputs, item.name);
      vars[item.name] = hasValue ? String(inputs[item.name] ?? "") : (item.default ?? "");
    }
  } else if (inputs && typeof inputs === "object") {
    for (const [name, value] of Object.entries(inputs)) {
      if (name !== "repo" && value !== undefined && value !== null) vars[name] = String(value);
    }
  }
  const body = { key, preset, vars, prompt: prompt.trim() };
  const trimmedHarness = String(harness ?? "").trim();
  const trimmedModel = String(model ?? "").trim();
  if (trimmedHarness) body.harness = trimmedHarness;
  if (trimmedModel) body.model = trimmedModel;
  return body;
}

// The form can start a job once it has a preset, a repository, a prompt, and
// every required preset input.
export function canStart({ preset, repo, prompt, inputs = {}, presetInputs: declared = [] }) {
  if (!preset || !repo || !prompt.trim()) return false;
  for (const item of declared) {
    if (item.required && !String(inputs?.[item.name] ?? "").trim()) return false;
  }
  return true;
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
