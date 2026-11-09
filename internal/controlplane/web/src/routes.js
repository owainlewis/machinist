const pages = new Set(["assign", "work", "automations", "runners"]);

export function routeFromHash(hash) {
  const value = hash.replace(/^#\//, "");
  if (value.startsWith("work/") && value.slice(5)) {
    try {
      return { view: "ticket", ticketID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "work", ticketID: "" };
    }
  }
  return { view: pages.has(value) ? value : "assign", ticketID: "" };
}
