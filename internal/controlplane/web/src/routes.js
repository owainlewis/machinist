import { settingsPages } from "./settings.js";

const pages = new Set(["dashboard", "jobs", "new", "runners"]);
const settingsPageNames = new Set(settingsPages.map(([page]) => page));

// #/dashboard shows the numbers and is where the app opens, #/jobs lists jobs, #/jobs/<id> opens one, #/new starts
// one, #/runners lists runners and #/settings/<page> shows one page of settings.
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
  if (value === "settings" || value.startsWith("settings/")) {
    const page = value.slice("settings/".length);
    return { view: "settings", jobID: "", page: settingsPageNames.has(page) ? page : "overview" };
  }
  return { view: pages.has(value) ? value : "dashboard", jobID: "" };
}

// redirectFor is the hash a route moved to, or "" when it has not moved. The
// old setup page is now the Settings overview.
export function redirectFor(hash) {
  const value = hash.replace(/^#\/?/, "");
  if (value === "setup" || value.startsWith("setup/")) return settingsHref("overview");
  const route = routeFromHash(hash);
  if (route.view === "settings" && hash !== settingsHref(route.page)) return settingsHref(route.page);
  return "";
}

export function jobHref(id) {
  return `#/jobs/${encodeURIComponent(id)}`;
}

export function settingsHref(page) {
  return `#/settings/${page}`;
}
