// The Settings area's pages and words, kept apart from the pages so they can be tested.

// settingsPages lists each page by its route and its name in the side nav.
export const settingsPages = [
  ["overview", "Overview"],
  ["projects", "Projects"],
  ["github", "GitHub"],
  ["linear", "Linear"],
  ["tokens", "Tokens"],
  ["team", "Team"],
  ["presets", "Presets and agents"],
  ["account", "My Account"],
];

const adminOnlySettings = new Set(["overview", "projects", "github", "linear", "tokens", "team"]);

// visibleSettingsPages filters Settings tabs by RBAC role.
export function visibleSettingsPages(role = "admin") {
  if (role === "admin") return settingsPages;
  return settingsPages.filter(([name]) => !adminOnlySettings.has(name));
}

// defaultSettingsPage chooses the default Settings tab for a role.
export function defaultSettingsPage(role = "admin") {
  return role === "admin" ? "overview" : "account";
}

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

// tokenPrefixLabel formats a token's stored prefix for display, or says when a
// legacy token has none.
export function tokenPrefixLabel(prefix) {
  return prefix ? `${prefix}…` : "No prefix";
}

// validateTokenName trims a token name and reports any problem before sending.
export function validateTokenName(value = "") {
  const name = value.trim();
  if (!name) return { name: "", error: "" };
  if (name.includes("/")) return { name, error: "Token names cannot contain /." };
  return { name, error: "" };
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

// stepKind classifies a preset step as a direct agent step, a shell script
// that invokes agents, or a pure shell step.
export function stepKind(step) {
  const agents = step?.agents || [];
  if (!agents.length) return "shell";
  const run = (step?.run || "").trim();
  const scripts = step?.scripts || [];
  if (!scripts.length && agents.length === 1 && (!run || run === `machinist agent ${agents[0].name}`)) {
    return "agent";
  }
  return "script_agent";
}

export function stepKindLabel(step) {
  const kind = stepKind(step);
  if (kind === "agent") return "Agent";
  if (kind === "script_agent") return "Script · Agent";
  return "Shell";
}

// stepShellPreview is the first line of a shell-only step's command, for its
// compact pipeline node.
export function stepShellPreview(step) {
  if ((step?.agents || []).length > 0) return "";
  return (step?.run || "").trim().split("\n")[0]?.trim() || "";
}

// durationShort formats milliseconds as a compact duration such as "5m" or "1h 30m".
export function durationShort(millis) {
  if (!millis || millis <= 0) return "";
  const totalSeconds = Math.round(millis / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const parts = [];
  if (hours) parts.push(`${hours}h`);
  if (minutes) parts.push(`${minutes}m`);
  if (seconds && !hours) parts.push(`${seconds}s`);
  return parts.join(" ");
}

// agentOverridden reports whether a step runs an agent on a different harness
// or model than the agent file's own front matter.
export function agentOverridden(stepAgent, fileAgent) {
  if (!stepAgent || !fileAgent) return false;
  return stepAgent.harness !== fileAgent.harness || (stepAgent.model || "") !== (fileAgent.model || "");
}

// agentFrontMatter formats an agent file's front matter fields as YAML lines.
export function agentFrontMatter(agent) {
  if (!agent) return "";
  const lines = [];
  if (agent.harness) lines.push(`harness: ${agent.harness}`);
  if (agent.model) lines.push(`model: ${agent.model}`);
  if (agent.command) lines.push(`command: ${agent.command}`);
  return lines.join("\n");
}

// groupAgents organizes agent files so reusable roles stand out, agents with
// identical prompts collapse into one group, preset-specific agents group by
// preset, and unused agents can stay collapsed.
export function groupAgents(agents = []) {
  const active = [];
  const unused = [];
  for (const agent of agents) {
    if ((agent.presets || []).length > 0) active.push(agent);
    else unused.push(agent);
  }

  const byPrompt = new Map();
  const activeBuckets = [];
  for (const agent of active) {
    const promptKey = (agent.prompt || "").trim();
    if (promptKey) {
      let bucket = byPrompt.get(promptKey);
      if (!bucket) {
        bucket = [];
        byPrompt.set(promptKey, bucket);
        activeBuckets.push(bucket);
      }
      bucket.push(agent);
    } else {
      activeBuckets.push([agent]);
    }
  }

  const makeGroup = variants => {
    const sorted = [...variants].sort((a, b) =>
      (b.presets?.length || 0) - (a.presets?.length || 0) ||
      a.name.length - b.name.length ||
      a.name.localeCompare(b.name),
    );
    const primary = sorted[0];
    const presetSet = new Set();
    const uses = [];
    for (const variant of sorted) {
      for (const preset of variant.presets || []) presetSet.add(preset);
      const variantUses = variant.uses?.length
        ? variant.uses
        : (variant.presets || []).map(preset => ({ preset, step: "", harness: variant.harness, model: variant.model || "" }));
      for (const use of variantUses) uses.push({ ...use, agent: variant.name });
    }
    const harnesses = [...new Set(sorted.map(variant => variant.harness).filter(Boolean))];
    return {
      key: primary.name,
      primary,
      variants: sorted,
      presets: [...presetSet].sort((a, b) => a.localeCompare(b)),
      uses,
      harnesses,
      prompt: (primary.prompt || "").trim(),
    };
  };

  const groups = activeBuckets.map(makeGroup).sort((a, b) => a.primary.name.localeCompare(b.primary.name));
  const shared = [];
  const singlePreset = [];
  for (const group of groups) {
    if (group.presets.length > 1 || group.variants.length > 1) shared.push(group);
    else singlePreset.push(group);
  }

  const byPreset = new Map();
  for (const group of singlePreset) {
    const preset = group.presets[0];
    if (!byPreset.has(preset)) byPreset.set(preset, []);
    byPreset.get(preset).push(group);
  }
  const presetNames = [...byPreset.keys()].sort((a, b) => a.localeCompare(b));
  const unusedGroups = unused.map(agent => makeGroup([agent])).sort((a, b) => a.primary.name.localeCompare(b.primary.name));

  if (shared.length > 0) {
    return {
      primary: shared,
      presetGroups: presetNames.map(preset => ({ preset, groups: byPreset.get(preset) })),
      unused: unusedGroups,
      hasShared: true,
    };
  }
  if (presetNames.length > 1) {
    return {
      primary: [],
      presetGroups: presetNames.map(preset => ({ preset, groups: byPreset.get(preset) })),
      unused: unusedGroups,
      hasShared: false,
    };
  }
  return {
    primary: singlePreset,
    presetGroups: [],
    unused: unusedGroups,
    hasShared: false,
  };
}
