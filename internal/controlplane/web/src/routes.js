const pages = new Set(["repositories", "runners", "setup", "agents", "automations", "settings", "runs", "dashboard", "analytics", "workers", "triggers"]);

export function routeFromHash(hash) {
  const value = hash.replace(/^#\//, "");
  if (value.startsWith("runs/") && value.slice(5)) {
    try {
      return { view: "task", jobID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "runs", jobID: "" };
    }
  }
  if (!value || value === "analytics" || value === "dashboard") return { view: "dashboard", jobID: "" };
  if (value === "inbox") return { view: "runs", jobID: "", filter: "attention" };
  if (value.startsWith("runs?")) {
    const filter = new URLSearchParams(value.slice(5)).get("filter");
    return ["active", "pr_ready", "attention", "failed", "succeeded"].includes(filter) ? { view: "runs", jobID: "", filter } : { view: "runs", jobID: "" };
  }
  return { view: pages.has(value) ? value : "runs", jobID: "" };
}
