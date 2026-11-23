// The UI speaks in job states, the same ones the run API returns.
export const stateLabels = {
  queued: "Queued",
  running: "Running",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

// jobState mirrors jobState in internal/controlplane/run_http.go. A working
// job is running once a runner holds it, and queued until then.
export function jobState(ticket, running = false) {
  switch (ticket?.state) {
    case "working": return running ? "running" : "queued";
    case "pr_ready":
    case "done": return "done";
    case "cancelled": return "cancelled";
    default: return "failed";
  }
}

export function isFinished(state) {
  return ["done", "failed", "cancelled"].includes(state);
}

const prStateLabels = { open: "Open", merged: "Merged", closed: "Closed" };

// prState is what Machinist last read about the job's pull request on GitHub:
// Open, Merged or Closed. A job from before Machinist tracked it says nothing.
export function prState(ticket) {
  return ticket?.pr_number ? prStateLabels[ticket.pr_state] || "" : "";
}

// prSummary is the pull request's number and state, such as "#12 · Merged".
export function prSummary(ticket) {
  if (!ticket?.pr_number) return "";
  const state = prState(ticket);
  return state ? `#${ticket.pr_number} · ${state}` : `#${ticket.pr_number}`;
}

// noPullRequest is what a job without a pull request shows in its place. One
// still going may open one yet; one that ended never will.
export function noPullRequest(state) {
  return isFinished(state) ? "None" : "None yet";
}

export function prLink(ticket) {
  return ticket?.pr_number ? `https://github.com/${ticket.repo}/pull/${ticket.pr_number}` : "";
}

// The jobs a runner holds now.
export function runningTickets(runners = []) {
  return new Set(runningJobs(runners).keys());
}

// What each running job is doing, by its job ID: the step it is on, when that
// step started, and the last action its agent named.
export function runningJobs(runners = []) {
  return new Map(runners.flatMap(runner => (runner.jobs || []).map(job => [job.ticket_id, job])));
}

// The newest job first, as the run API lists them.
export function newestFirst(tickets = []) {
  return [...tickets].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || (a.id < b.id ? 1 : -1));
}

export function formatDuration(milliseconds) {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "";
  const seconds = Math.floor(milliseconds / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

// The board's columns. Queued jobs run oldest first, so the next to run is on
// top. Running and ended jobs show the latest first. Done holds every ended
// job, and its pill says whether it passed, failed or was cancelled.
export const boardColumns = [
  { key: "queued", label: "Queued" },
  { key: "running", label: "Running" },
  { key: "done", label: "Done" },
];

export function boardOf(tickets = [], running = new Set()) {
  const columns = Object.fromEntries(boardColumns.map(column => [column.key, []]));
  for (const ticket of tickets) {
    const state = jobState(ticket, running.has(ticket.id));
    columns[isFinished(state) ? "done" : state].push({ ticket, state });
  }
  const at = (job, field) => Date.parse(job.ticket[field]) || 0;
  columns.queued.sort((a, b) => at(a, "created_at") - at(b, "created_at"));
  columns.running.sort((a, b) => at(b, "updated_at") - at(a, "updated_at"));
  columns.done.sort((a, b) => at(b, "updated_at") - at(a, "updated_at"));
  return columns;
}

// failureReason is why a failed job stopped: the last thing Machinist said on it.
export function failureReason(entries = []) {
  return entries.findLast(entry => entry.kind === "message")?.body || "";
}

// linkParts splits text into plain runs and web links, so the links can be
// clicked. A full stop, comma or bracket at a link's end is left as text.
const linkPattern = /https?:\/\/[^\s<>"]+/g;
export function linkParts(text = "") {
  const parts = [];
  let at = 0;
  for (const match of text.matchAll(linkPattern)) {
    const href = match[0].replace(/[.,;:!?)\]}'"]+$/, "");
    if (match.index > at) parts.push({ text: text.slice(at, match.index) });
    parts.push({ href });
    at = match.index + href.length;
  }
  if (at < text.length) parts.push({ text: text.slice(at) });
  return parts;
}

// Where a job came from, as the API names it: the web app, the run API, a
// schedule, or a tracker such as Linear. A tracker added later shows its own
// name until it gets a label here.
export const sourceLabels = {
  web: "Web",
  api: "API",
  schedule: "Schedule",
  linear: "Linear",
  jira: "Jira",
  github: "GitHub",
  slack: "Slack",
};

const sourceOrder = Object.keys(sourceLabels);

export function sourceLabel(source) {
  if (!source) return "Unknown";
  return sourceLabels[source] || source.charAt(0).toUpperCase() + source.slice(1);
}

// sourceLink is the original issue or message, if the job has one. Only a web
// link is followed, so stored text can never become script.
export function sourceLink(ticket) {
  const link = ticket?.source_url || "";
  return /^https?:\/\//i.test(link) ? link : "";
}

// sourceLinkText names where a job's original lives, for its link: "Open in
// Linear", "Open in GitHub", or the site's own name.
const siteNames = { "linear.app": "Linear", "github.com": "GitHub", "atlassian.net": "Jira", "slack.com": "Slack" };
export function sourceLinkText(link) {
  let host;
  try { host = new URL(link).hostname.replace(/^www\./, ""); } catch { return "Open the original"; }
  const site = Object.keys(siteNames).find(name => host === name || host.endsWith(`.${name}`));
  return `Open in ${site ? siteNames[site] : host}`;
}

// sourcesOf lists the sources the jobs came from, known ones first in a fixed
// order, so the filter's options do not move about as jobs arrive.
export function sourcesOf(tickets = []) {
  const found = new Set(tickets.map(ticket => ticket.source).filter(Boolean));
  const rank = source => sourceOrder.includes(source) ? sourceOrder.indexOf(source) : sourceOrder.length;
  return [...found].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

// The quick filters above the jobs. "Needs a person" is a job whose pull
// request is open and waits for someone to review and merge it.
export const quickFilters = [
  { key: "running", label: "Running" },
  { key: "done", label: "Done" },
  { key: "failed", label: "Failed" },
  { key: "needs_person", label: "Needs a person" },
  { key: "cancelled", label: "Cancelled" },
];

export function matchesQuick(ticket, running, quick) {
  if (!quick) return true;
  if (quick === "needs_person") return ticket.state === "pr_ready";
  return jobState(ticket, running) === quick;
}

// filterJobs keeps the jobs from one source, in one quick filter. Either may be
// empty, which keeps every job.
export function filterJobs(tickets = [], { source = "", quick = "" } = {}, running = new Set()) {
  return tickets.filter(ticket => (!source || ticket.source === source) && matchesQuick(ticket, running.has(ticket.id), quick));
}

// quickCounts counts the jobs in each quick filter, within a source.
export function quickCounts(tickets = [], source = "", running = new Set()) {
  const counts = Object.fromEntries(quickFilters.map(({ key }) => [key, 0]));
  for (const ticket of filterJobs(tickets, { source }, running)) {
    for (const { key } of quickFilters) if (matchesQuick(ticket, running.has(ticket.id), key)) counts[key]++;
  }
  return counts;
}

// prChip is what a job's pull request chip shows: its number, its state on
// GitHub, and where it links. A job without a pull request has none.
export function prChip(ticket) {
  if (!ticket?.pr_number) return null;
  const state = prStateLabels[ticket.pr_state] ? ticket.pr_state : "";
  return { number: ticket.pr_number, state, label: prState(ticket), href: prLink(ticket) };
}
