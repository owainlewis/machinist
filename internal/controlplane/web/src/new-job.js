// randomHex uses getRandomValues, which works on plain http too. randomUUID
// needs a secure context, and a control plane on a LAN address is not one.
function randomHex() {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
}

// The run API takes a key so a retried request starts one job, not two. The
// form makes one key when it opens, so a double click is harmless.
export function newJobKey(random = randomHex) {
  return `ui-${random()}`;
}

// runRequest is the body of POST /machinist/run for what the form holds.
export function runRequest({ key, preset, repo, prompt }) {
  return { key, preset, vars: { repo }, prompt: prompt.trim() };
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
