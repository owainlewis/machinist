const pages = new Set(["dashboard", "jobs", "new", "runners", "setup"]);

// #/dashboard shows the numbers and is where the app opens, #/jobs lists jobs, #/jobs/<id> opens one, #/new starts
// one, #/runners lists runners and #/setup shows what is set up and the projects.
export function routeFromHash(hash) {
  const value = hash.replace(/^#\//, "");
  if (value.startsWith("jobs/") && value.slice(5)) {
    try {
      return { view: "job", jobID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "jobs", jobID: "" };
    }
  }
  if (value === "jobs/") return { view: "jobs", jobID: "" };
  return { view: pages.has(value) ? value : "dashboard", jobID: "" };
}

export function jobHref(id) {
  return `#/jobs/${encodeURIComponent(id)}`;
}
