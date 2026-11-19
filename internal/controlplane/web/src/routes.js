const pages = new Set(["dashboard", "jobs", "new", "runners"]);

// #/dashboard shows the numbers and is where the app opens, #/jobs lists jobs, #/jobs/<id> opens one, #/new starts
// one and #/runners lists runners.
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
