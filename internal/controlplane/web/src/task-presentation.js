export const stateLabels = {
  working: "Working",
  needs_you: "Needs you",
  pr_ready: "PR ready",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

const maxFinished = 25;
const activeOrder = { needs_you: 0, working: 1, pr_ready: 2 };

// Work that still needs someone comes first, then the latest finished work.
export function sortTickets(tickets) {
  const active = tickets.filter(isActive).sort((a, b) => activeOrder[a.state] - activeOrder[b.state]);
  return [...active, ...tickets.filter(ticket => !isActive(ticket)).slice(0, maxFinished)];
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
