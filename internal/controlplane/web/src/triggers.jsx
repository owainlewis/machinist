import { ErrorBanner, QuietState, TopBar } from "@/components/ui/page-heading";
import { StatusIcon } from "@/components/ui/status-icon";
import { cn } from "@/lib/utils";
import { taskHref } from "@/routes";
import { humanize, jobDisplayTitle } from "@/runs-board";
import { triggerView } from "@/trigger-state";
import { relativeTime } from "./task-display.jsx";

// Automations are the configured triggers. Each run they start is a normal task.
export function AutomationsPage({ triggers = [], jobs = [], loaded, error }) {
  return <>
    <TopBar title="Automations"><span className="desktop-only text-xs text-faint">Defined in config.toml</span></TopBar>
    {error && <ErrorBanner>{error}</ErrorBanner>}
    <div className="pane-scroll">
      {!loaded && !error ? <QuietState title="Checking the schedule" description="Loading automation state." loading role="status" />
        : loaded && (triggers.length ? triggers.map((trigger) => <AutomationRow key={trigger.identity} trigger={trigger} jobs={jobs} />)
        : <QuietState title="No automations" description="Add a [triggers.interval.NAME] or [triggers.cron.NAME] block to config.toml to run a prompt on a schedule." />)}
    </div>
  </>;
}

function AutomationRow({ trigger, jobs }) {
  const view = triggerView(trigger);
  const active = trigger.active_job ? jobs.find((job) => job.id === trigger.active_job) : undefined;
  const runs = jobs.filter((job) => job.trigger_id === trigger.identity);
  const state = active ? active.state : view.health === "failed" ? "failed" : view.health === "stale" ? "needs" : runs.length ? "done" : "idle";
  const [family, name = view.identity] = view.identity.split("/", 2);
  const next = trigger.next_due ? `next ${relativeFuture(trigger.next_due)}` : "";
  const last = trigger.last_success ? `last succeeded ${relativeTime(trigger.last_success)}` : trigger.admission_count ? "" : "never run";
  return <article className="row items-start py-2.5">
    <span className="mt-0.5"><StatusIcon state={state} /></span>
    <div className="row-main gap-0.5">
      <h2 className="row-title">{humanize(name)} <span className="font-mono text-[0.6875rem] text-faint">{family}</span></h2>
      <p className={cn("row-reason", view.error && "tone-danger")}>{view.error || [active ? "Running now" : "", next, last].filter(Boolean).join(" · ")}</p>
      {active && <a href={taskHref(active.id)} className="row-reason text-muted-foreground hover:text-foreground hover:underline">{jobDisplayTitle(active)}</a>}
    </div>
    <div className="row-meta">
      <span className="desktop-only">{trigger.admission_count || 0} run{trigger.admission_count === 1 ? "" : "s"}</span>
      <span className="desktop-only capitalize">{view.health}</span>
    </div>
  </article>;
}

function relativeFuture(value) {
  const seconds = Math.round((Date.parse(value) - Date.now()) / 1000);
  if (!Number.isFinite(seconds)) return "unknown";
  if (seconds <= 0) return "now";
  if (seconds < 60) return `in ${seconds}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `in ${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `in ${hours}h`;
  return `in ${Math.round(hours / 24)}d`;
}
