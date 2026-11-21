// The setup checklist's words, kept apart from the page so they can be tested.

// missingSteps lists the required checks that fail.
export function missingSteps(setup) {
  return (setup?.checks || []).filter(check => check.required && !check.ok);
}

// setupLine says how far setup has got, in one short sentence.
export function setupLine(setup) {
  const required = (setup?.checks || []).filter(check => check.required);
  const done = required.filter(check => check.ok).length;
  if (done === required.length) return "Machinist is ready to take jobs.";
  return `${done} of ${required.length} required steps are done.`;
}

// repoName turns what a person typed or pasted into owner/name. It returns ""
// when the text is not a repository.
export function repoName(text) {
  const value = text.trim().replace(/^https?:\/\/github\.com\//i, "").replace(/\.git$/i, "").replace(/\/+$/, "");
  return /^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$/.test(value) ? value : "";
}

// shouldOpenSetup is true when someone opens the app at its start page and a
// required step is missing, so a new install starts on the checklist.
export function shouldOpenSetup(setup, hash) {
  return missingSteps(setup).length > 0 && ["", "#", "#/", "#/dashboard"].includes(hash);
}
