const pages = new Set(["runs", "analytics", "workers", "triggers", "agents", "pipelines"]);

export function routeFromHash(hash) {
  const value = hash.replace(/^#\//, "");
  if (value.startsWith("runs/") && value.slice(5)) {
    try {
      return { view: "task", jobID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "runs", jobID: "" };
    }
  }
  return { view: pages.has(value) ? value : "runs", jobID: "" };
}
