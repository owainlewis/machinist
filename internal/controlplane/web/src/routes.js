const pages = new Set(["home", "tasks", "automations", "workers", "usage", "settings"]);
// Older links keep working after the redesign.
const aliases = { runs: "tasks", triggers: "automations", analytics: "usage", workflows: "settings", commands: "settings" };

export function routeFromHash(hash) {
  const value = hash.replace(/^#\/?/, "");
  const detail = value.match(/^(?:tasks|runs)\/(.+)$/);
  if (detail) {
    try {
      return { view: "task", jobID: decodeURIComponent(detail[1]) };
    } catch {
      return { view: "tasks", jobID: "" };
    }
  }
  if (pages.has(value)) return { view: value, jobID: "" };
  if (aliases[value]) return { view: aliases[value], jobID: "" };
  if (value === "tasks/" || value === "runs/") return { view: "tasks", jobID: "" };
  return { view: "home", jobID: "" };
}

export const taskHref = (id) => `#/tasks/${encodeURIComponent(id)}`;
