// The harnesses Machinist runs, by the name an agent file gives.
const harnesses = { claude: "Claude Code", codex: "Codex", agy: "Antigravity", pi: "Pi" };

export function harnessLabel(harness = "") {
  return harnesses[harness] || harness;
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
