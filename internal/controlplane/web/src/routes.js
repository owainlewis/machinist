const pages = new Set(["jobs", "runners"]);

// #/jobs lists jobs, #/jobs/<id> opens one and #/runners lists runners.
export function routeFromHash(hash) {
  const value = hash.replace(/^#\//, "");
  if (value.startsWith("jobs/") && value.slice(5)) {
    try {
      return { view: "job", jobID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "jobs", jobID: "" };
    }
  }
  return { view: pages.has(value) ? value : "jobs", jobID: "" };
}

export function jobHref(id) {
  return `#/jobs/${encodeURIComponent(id)}`;
}
