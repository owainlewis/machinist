import React from "react";
import { Ban, Check, Clock3, LoaderCircle, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { stateLabels } from "./jobs.js";

const glyphs = {
  queued: [Clock3, ""],
  running: [LoaderCircle, "pill-info"],
  done: [Check, "pill-success"],
  failed: [X, "pill-danger"],
  cancelled: [Ban, ""],
};

export function State({ value }) {
  const [Icon, tone] = glyphs[value] || glyphs.cancelled;
  return <span className={cn("pill", tone)}>
    <Icon className={cn("size-3.5", value === "running" && "state-working")} aria-hidden="true" />
    <span>{stateLabels[value] || value}</span>
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
