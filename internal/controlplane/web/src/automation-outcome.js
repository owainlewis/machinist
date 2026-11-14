export function outcomeText(value) {
  if (typeof value !== "string") return "";
  const plain = value.replace(/\s+/g, " ").trim();
  return plain.length > 240 ? `${plain.slice(0, 239)}…` : plain;
}

export function automationOutcome(automation) {
  const ticket = automation.last_ticket;
  if (!ticket) return { text: "Never run" };
  const link = { href: `#/work/${encodeURIComponent(ticket.id)}`, label: "View ticket" };
  const said = outcomeText(ticket.outcome);
  switch (ticket.state) {
    case "working": return { text: "Running now.", link, active: true };
    case "needs_you": return { text: said || "Waiting for a reply.", link, active: true };
    case "pr_ready": return { text: "Opened a PR.", link: { href: `https://github.com/${ticket.repo}/pull/${ticket.pr_number}`, label: "View PR", external: true }, active: true };
    case "failed": return { text: said ? `Failed: ${said}` : "Failed. Open the ticket to see why.", link };
    case "cancelled": return { text: "Cancelled.", link };
    default: return { text: said || "Finished.", link };
  }
}
