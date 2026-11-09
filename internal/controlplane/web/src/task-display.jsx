import React from "react";
import { Ban, Check, CircleHelp, GitPullRequest, LoaderCircle, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { stateLabels } from "./task-presentation.js";

const glyphs = {
  working: [LoaderCircle, "text-primary"],
  needs_you: [CircleHelp, "text-warning"],
  pr_ready: [GitPullRequest, "text-info"],
  done: [Check, "text-success"],
  failed: [X, "text-danger"],
  cancelled: [Ban, "text-muted-foreground"],
};

export function State({ value, label = true }) {
  const [Icon, tone] = glyphs[value] || glyphs.cancelled;
  return <span className={cn("ticket-state", tone)}>
    <Icon className={cn("size-3.5", value === "working" && "state-working")} aria-hidden="true" />
    {label ? <span>{stateLabels[value] || value}</span> : <span className="sr-only">{stateLabels[value] || value}</span>}
  </span>;
}

export function relativeTime(value, now = Date.now()) {
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return "Never";
  const seconds = Math.max(0, Math.floor((now - time) / 1000));
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function formatTimestamp(value) {
  return Number.isFinite(Date.parse(value)) ? new Date(value).toLocaleString() : "Unavailable";
}
