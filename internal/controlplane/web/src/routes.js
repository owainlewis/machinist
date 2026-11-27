import { defaultSettingsPage, settingsPages, visibleSettingsPages } from "./settings.js";

const pages = new Set(["dashboard", "jobs", "automations", "new", "runners"]);
const settingsPageNames = new Set(settingsPages.map(([page]) => page));

// #/dashboard shows the numbers and is where the app opens, #/jobs lists jobs, #/jobs/<id> opens one, #/automations
// lists automations, #/new starts a job, #/runners lists runners and #/settings/<page> shows one page of settings.
export function routeFromHash(hash, role = "admin") {
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
    const allowed = new Set(visibleSettingsPages(role).map(([name]) => name));
    const fallback = defaultSettingsPage(role);
    return { view: "settings", jobID: "", page: settingsPageNames.has(page) && allowed.has(page) ? page : fallback };
  }
  return { view: pages.has(value) ? value : "dashboard", jobID: "" };
}

// redirectFor is the hash a route moved to, or "" when it has not moved. The
// old setup page is now the Settings overview.
export function redirectFor(hash, role = "admin") {
  const value = hash.replace(/^#\/?/, "");
  if (value === "setup" || value.startsWith("setup/")) return settingsHref(defaultSettingsPage(role));
  if (role === "viewer" && value === "new") return "#/jobs";
  const route = routeFromHash(hash, role);
  if (route.view === "settings" && hash !== settingsHref(route.page)) return settingsHref(route.page);
  return "";
}

export function jobHref(id) {
  return `#/jobs/${encodeURIComponent(id)}`;
}

export function settingsHref(page) {
  return `#/settings/${page}`;
}
