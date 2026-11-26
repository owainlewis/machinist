import React from "react";
import { mergeGateRisk } from "./jobs.js";

// RiskBadge is the merge gate's risk level as a compact pill: Low (emerald),
// Medium (amber), or High (rose). Jobs without a merge-gate risk show nothing.
export function RiskBadge({ ticket }) {
  const risk = mergeGateRisk(ticket);
  if (!risk) return null;
  const reason = typeof ticket?.merge_gate_reason === "string" ? ticket.merge_gate_reason.trim() : "";
  const title = reason ? `Merge gate (${risk.label} risk): ${reason}` : `Merge gate: ${risk.label} risk`;
  return <span className="risk-badge" data-risk={risk.level} title={title} aria-label={`Risk: ${risk.label}`}>
    <span className="risk-badge-dot" aria-hidden="true" />
    <span>{risk.label}</span>
  </span>;
}
