export const stateLabels = {
  working: "Working",
  needs_you: "Needs you",
  pr_ready: "PR ready",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

const maxFinished = 25;

// Work is ordered by who has to act next, not by when it started.
export function groupTickets(tickets) {
  const pick = (...states) => tickets.filter(ticket => states.includes(ticket.state));
  return [
    { id: "you", title: "Waiting on you", tickets: pick("needs_you") },
    { id: "merge", title: "Waiting for a merge", tickets: pick("pr_ready") },
    { id: "agent", title: "Waiting on an agent", tickets: pick("working") },
    { id: "finished", title: "Finished", tickets: pick("done", "failed", "cancelled").slice(0, maxFinished) },
  ].filter(group => group.tickets.length);
}

export function isActive(ticket) {
  return ["working", "needs_you", "pr_ready"].includes(ticket?.state);
}

export function issueLink(ticket) {
  return ticket.issue_number ? `https://github.com/${ticket.repo}/issues/${ticket.issue_number}` : "";
}

export function prLink(ticket) {
  return ticket.pr_number ? `https://github.com/${ticket.repo}/pull/${ticket.pr_number}` : "";
}

export function ticketSource(ticket) {
  if (ticket.issue_number) return `Issue #${ticket.issue_number}`;
  if (ticket.source?.startsWith("schedule:")) return "Automation";
  return "Assigned here";
}
