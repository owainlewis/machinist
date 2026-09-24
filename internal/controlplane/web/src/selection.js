// Workflows and commands are both startable. Workflows come first because they
// carry the task fields; commands stay available for one-off work.
export function selectionChoices(status) {
  const workflows = (status.workflows || []).map(name => ({ value: `workflow:${name}`, name, group: "Workflows" }));
  const commands = (status.commands || []).map(name => ({ value: `command:${name}`, name, group: "Commands" }));
  return [...workflows, ...commands];
}

// Prefer the plain run choice, then the first available choice.
export function firstSelection(status) {
  const choices = selectionChoices(status);
  return (choices.find(c => c.value === "workflow:run") || choices.find(c => c.value === "command:run") || choices[0])?.value || "";
}
