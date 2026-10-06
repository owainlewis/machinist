import React from "react";
import { StatusIcon } from "@/components/ui/status-icon";
const zeroTime = "0001-01-01T00:00:00Z";

export function State({ value }) {
  return <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><StatusIcon state={value} />{stateLabel(value)}</span>;
}

export function friendlyName(name) {
  return String(name || "")
    .replaceAll("_", " ")
    .replaceAll("-", " ")
    .replace(/^./, (c) => c.toUpperCase());
}
export function relativeTime(value) {
  if (!value || value === zeroTime) return "Not started";
  const seconds = Math.max(
    0,
    Math.floor((Date.now() - Date.parse(value)) / 1000),
  );
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}
export function formatTimestamp(value) {
  return !value || value === zeroTime || !Number.isFinite(Date.parse(value))
    ? "Unavailable"
    : new Date(value).toLocaleString();
}
export function stateLabel(value) {
  const labels = { awaiting_approval: "Needs approval", timed_out: "Timed out", succeeded: "Done", blocked: "Needs you" };
  return labels[value] || friendlyName(value || "unknown");
}
export function shortId(id) {
  const [, value = id] = String(id).split("_", 2);
  return value.slice(0, 8);
}
