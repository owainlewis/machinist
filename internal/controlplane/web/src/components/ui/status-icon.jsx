import { cn } from "@/lib/utils";
import { statusGroup } from "@/runs-board";

const labels = { queued: "Queued", running: "Running", needs: "Needs you", failed: "Failed", done: "Done", cancelled: "Cancelled", idle: "Idle", paused: "Paused", online: "Online", offline: "Offline" };

// StatusIcon draws the one glyph set used everywhere. Pass a job state or a group id.
export function StatusIcon({ state, className, title }) {
  const kind = glyphs[state] ? state : statusGroup(state);
  const label = title || labels[kind];
  return <svg className={cn("status-icon", kind === "running" && "spinner", className)} viewBox="0 0 14 14" role="img" aria-label={label}>
    <title>{label}</title>
    {glyphs[kind] || glyphs.idle}
  </svg>;
}

// Spinner is the running glyph on its own, for buttons and loading states.
export function Spinner({ className, label = "Loading" }) {
  return <svg className={cn("status-icon spinner", className)} viewBox="0 0 14 14" role="img" aria-label={label}>{glyphs.running}</svg>;
}

const glyphs = {
  queued: <circle cx="7" cy="7" r="5.5" fill="none" stroke="var(--faint)" strokeWidth="1.5" strokeDasharray="2 2" />,
  running: <>
    <circle cx="7" cy="7" r="5.5" fill="none" stroke="color-mix(in srgb, var(--primary) 25%, transparent)" strokeWidth="1.5" />
    <path d="M7 1.5a5.5 5.5 0 0 1 5.5 5.5" fill="none" stroke="var(--primary)" strokeWidth="1.5" strokeLinecap="round" />
  </>,
  needs: <>
    <circle cx="7" cy="7" r="6.5" fill="var(--warning)" />
    <path d="M7 3.8v4M7 10h.01" stroke="var(--surface)" strokeWidth="1.6" strokeLinecap="round" />
  </>,
  done: <>
    <circle cx="7" cy="7" r="6.5" fill="var(--success)" />
    <path d="M4.3 7.2l1.8 1.8 3.6-3.8" fill="none" stroke="var(--surface)" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </>,
  failed: <>
    <circle cx="7" cy="7" r="6.5" fill="var(--danger)" />
    <path d="M4.8 4.8l4.4 4.4M9.2 4.8l-4.4 4.4" stroke="var(--surface)" strokeWidth="1.6" strokeLinecap="round" />
  </>,
  cancelled: <>
    <circle cx="7" cy="7" r="5.5" fill="none" stroke="var(--faint)" strokeWidth="1.5" />
    <path d="M4.5 9.5l5-5" stroke="var(--faint)" strokeWidth="1.5" strokeLinecap="round" />
  </>,
  paused: <>
    <circle cx="7" cy="7" r="5.5" fill="none" stroke="var(--faint)" strokeWidth="1.5" />
    <path d="M5.8 5v4M8.2 5v4" stroke="var(--faint)" strokeWidth="1.4" />
  </>,
  idle: <circle cx="7" cy="7" r="5.5" fill="none" stroke="var(--faint)" strokeWidth="1.5" />,
  online: <circle cx="7" cy="7" r="3.5" fill="var(--success)" />,
  offline: <circle cx="7" cy="7" r="3.5" fill="none" stroke="var(--faint)" strokeWidth="1.5" />,
};
