import React from "react";
import { GitMerge, GitPullRequest, GitPullRequestClosed } from "lucide-react";
import { prChip } from "./jobs.js";

const icons = { open: GitPullRequest, merged: GitMerge, closed: GitPullRequestClosed };

// PRChip is a job's pull request as a chip that links to it on GitHub, coloured
// by whether it is open, merged or closed.
export function PRChip({ ticket }) {
  const chip = prChip(ticket);
  if (!chip) return null;
  const Icon = icons[chip.state] || GitPullRequest;
  return <a className="pr-chip" data-state={chip.state || undefined} href={chip.href} target="_blank" rel="noreferrer" aria-label={`Pull request #${chip.number}${chip.label ? `, ${chip.label.toLowerCase()}` : ""}`}>
    <Icon className="size-3.5" aria-hidden="true" />
    <span>#{chip.number}</span>
    {chip.label && <span className="pr-chip-state">{chip.label}</span>}
  </a>;
}
