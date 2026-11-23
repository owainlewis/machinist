// The Settings area's pages and words, kept apart from the pages so they can be tested.

// settingsPages lists each page by its route and its name in the side nav.
export const settingsPages = [
  ["overview", "Overview"],
  ["projects", "Projects"],
  ["github", "GitHub"],
  ["linear", "Linear"],
  ["tokens", "Tokens"],
  ["presets", "Presets and agents"],
];

// checkLink is the page that holds more about a setup check, and what its
// link is called. Most are Settings pages. Runners have a page of their own.
const checkLinks = {
  GitHub: ["#/settings/github", "Open GitHub settings"],
  Projects: ["#/settings/projects", "Open Projects settings"],
  Linear: ["#/settings/linear", "Open Linear settings"],
  Agents: ["#/settings/presets", "Open Agents settings"],
  Factory: ["#/settings/presets", "Open Presets and agents"],
  Runner: ["#/runners", "Open Runners"],
};
export function checkLink(name) {
  return checkLinks[name] || ["", ""];
}

// pollLabel says how often Linear is asked, such as "Every 1m 30s".
export function pollLabel(seconds) {
  if (!seconds) return "";
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (!minutes) return `Every ${rest}s`;
  return rest ? `Every ${minutes}m ${rest}s` : `Every ${minutes}m`;
}

// roleLabel says what a token's role lets it do.
const roles = {
  operator: ["Person", "Signs in to the web app and uses the API and CLI."],
  runner: ["Runner", "Polls for runs and reports on them."],
  all: ["Person and runner", "Made before roles, so it can do both. Replace it with one of each."],
};
export function roleLabel(role) {
  return roles[role] || [role, ""];
}

// tokenLine says how many tokens are active and how many are revoked.
export function tokenLine(tokens = []) {
  const active = tokens.filter(token => !token.revoked_at).length;
  const revoked = tokens.length - active;
  const words = `${active} active ${active === 1 ? "token" : "tokens"}`;
  return revoked ? `${words}, ${revoked} revoked.` : `${words}.`;
}

const dateFormat = new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });

// dayLabel is a date a person reads, such as "Nov 20, 2026".
export function dayLabel(value) {
  const time = Date.parse(value);
  return Number.isFinite(time) ? dateFormat.format(time) : "";
}

// githubWarnings counts the repositories with a warning or a problem.
export function githubWarnings(settings) {
  return (settings?.repositories || []).filter(repo => repo.problem || repo.warnings?.length).length;
}

// githubLine says how runs reach GitHub, and how many repositories need a look.
export function githubLine(settings) {
  const how = settings.app ? "Runs use short-lived tokens from the GitHub App." : "Runs use each runner's own GH_TOKEN.";
  const count = githubWarnings(settings);
  if (!count) return how;
  return `${how} ${count} ${count === 1 ? "repository needs" : "repositories need"} a look.`;
}

// factoryUpdateLine says how many shipped factory files are out of date.
export function factoryUpdateLine(updates = []) {
  if (!updates.length) return "";
  return updates.length === 1
    ? "1 factory file is out of date."
    : `${updates.length} factory files are out of date.`;
}
