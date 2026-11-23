import React from "react";
import { CalendarClock, CircleDot, Globe, MessageSquare, SquareKanban, Terminal, Ticket, Webhook } from "lucide-react";
import { cn } from "@/lib/utils";
import { sourceLabel } from "./jobs.js";

// One icon for each place a job can come from. A tracker added later gets the
// generic mark until it has its own.
const icons = {
  web: Globe,
  api: Terminal,
  schedule: CalendarClock,
  linear: SquareKanban,
  jira: Ticket,
  github: CircleDot,
  slack: MessageSquare,
};

export function SourceIcon({ source, className = "size-3.5" }) {
  const Icon = icons[source] || Webhook;
  return <Icon className={className} aria-hidden="true" />;
}

// Source is a job's source as an icon and its name. With a link it opens the
// original issue or message.
export function Source({ source, href, compact = false, className }) {
  const body = <><SourceIcon source={source} /><span className={compact ? "sr-only" : undefined}>{sourceLabel(source)}</span></>;
  const title = compact ? sourceLabel(source) : undefined;
  if (href) return <a className={cn("source", className)} href={href} target="_blank" rel="noreferrer" title={title} data-source={source}>{body}</a>;
  return <span className={cn("source", className)} title={title} data-source={source}>{body}</span>;
}
