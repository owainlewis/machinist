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

export function prLink(ticket) {
  return ticket?.pr_number ? `https://github.com/${ticket.repo}/pull/${ticket.pr_number}` : "";
}

// The jobs a runner holds now.
export function runningTickets(runners = []) {
  return new Set(runners.flatMap(runner => (runner.jobs || []).map(job => job.ticket_id)));
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
