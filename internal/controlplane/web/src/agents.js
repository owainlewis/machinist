// The harnesses Machinist runs, by the name an agent file gives.
const harnesses = { claude: "Claude Code", codex: "Codex", agy: "Antigravity", pi: "Pi" };

export function harnessLabel(harness = "") {
  return harnesses[harness] || harness;
}

// harnessList names harnesses for a sentence: "Codex", "Antigravity and Codex".
export function harnessList(names = []) {
  const labels = names.map(harnessLabel);
  if (labels.length < 2) return labels.join("");
  return `${labels.slice(0, -1).join(", ")} and ${labels.at(-1)}`;
}

// labelList names host labels for a sentence: "label macos", "labels arm64 and macos".
export function labelList(labels = []) {
  if (!labels.length) return "";
  const joined = labels.length < 2 ? labels[0] : `${labels.slice(0, -1).join(", ")} and ${labels.at(-1)}`;
  return `${labels.length === 1 ? "label" : "labels"} ${joined}`;
}

// waitingForRunner says what a queued job waits for: any runner, or one with
// the harnesses and/or host labels no online runner has.
export function waitingForRunner(harnesses = [], labels = []) {
  const parts = [];
  if (harnesses.length) parts.push(harnessList(harnesses));
  if (labels.length) parts.push(labelList(labels));
  return parts.length ? `Waiting for a runner with ${parts.join(" and ")}` : "Waiting for a runner";
}

// agentSummary says what runs an agent: its model, or the harness's default
// model when the agent names none.
export function agentSummary(agent) {
  return agent.model || `${harnessLabel(agent.harness)} default`;
}

// agentTitle is the full description, for a tooltip or a screen reader.
export function agentTitle(agent) {
  return `Agent ${agent.name}, on ${harnessLabel(agent.harness)}, model ${agent.model || "the harness default"}`;
}

// presetAgents lists each agent a preset runs once, in the order its steps
// first run them.
export function presetAgents(preset) {
  const seen = new Map();
  for (const step of preset?.steps || []) {
    for (const agent of step.agents || []) {
      const key = `${agent.name}\u0000${agent.harness}\u0000${agent.model || ""}`;
      if (!seen.has(key)) seen.set(key, agent);
    }
  }
  return [...seen.values()];
}
