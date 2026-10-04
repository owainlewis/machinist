export function copyConfiguration(value) {
  return structuredClone(value);
}
export function configurationErrors(configuration) {
  const errors = [];
  for (const [id, agent] of Object.entries(configuration.agents || {})) {
    const name = agent.name || id;
    if (!agent.name?.trim()) errors.push(`Agent ${id} needs a name.`);
    if (!agent.prompt?.trim()) errors.push(`${name} needs instructions.`);
    if (!agent.timeout?.trim())
      errors.push(`${name} needs a timeout, such as 30m.`);
    if (agent.runtime !== "claude")
      errors.push(`${name} must use Claude Code.`);
  }
  for (const pipeline of Object.values(configuration.pipelines || {})) {
    if (!pipeline.name?.trim()) errors.push("Each pipeline needs a name.");
    for (const step of pipeline.steps || []) {
      if (!step.name?.trim()) errors.push(`Step ${step.id} needs a name.`);
      if (step.type === "agent" && !configuration.agents[step.agent])
        errors.push(`${step.name} needs an existing agent.`);
      if (step.type === "script" && !step.command?.[0]?.trim())
        errors.push(`${step.name} needs an executable on the first line.`);
    }
  }
  return errors;
}
export function commandLines(value) {
  return value.split("\n");
}
