import { useMemo, useState } from "react";
import { ArrowRight, Clock3, Plus, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PageHeading } from "@/components/ui/page-heading";
import { dashboardState, workQueueState, queueSummary } from "./dashboard-state.js";
import { jobDisplayTitle } from "./runs-board.js";
import { State } from "./task-display.jsx";
import { formatDurationMillis, formatTokenUsage, estimatedJobCost, formatEstimatedCost } from "./run-metrics.js";

const jobURL = job => `#/runs/${encodeURIComponent(job.id)}`;
export function Dashboard({ jobs = [], repositories = [], capacity, kubernetes, connected, loaded, error, onNewRun }) {
  const [project, setProject] = useState("");
  const view = workQueueState({ jobs, repositories, project, loaded, error });
  return <div className="runs-page dashboard-page">
    <PageHeading title="Dashboard" description="Your daily work queue.">
      <Button onClick={() => onNewRun?.(repositories.includes(project) ? project : undefined)}><Plus className="size-4" />New run</Button>
    </PageHeading>
    <div className="queue-toolbar"><label>Project <select value={project} onChange={event => setProject(event.target.value)}>
      <option value="">All projects</option>
      {view.projects.map(item => <option key={item.value} value={item.value}>{item.label}</option>)}
    </select></label><a href="#/runs">Run history</a><a href="#/activity">Activity</a></div>
    {Number.isSafeInteger(capacity?.effective_capacity_upper_bound) && <p className="queue-capacity">Connected runner capacity: up to {capacity.effective_capacity_upper_bound} active job{capacity.effective_capacity_upper_bound === 1 ? "" : "s"}. {kubernetes && capacity.kubernetes_credential_stream_parallelism === 1 && "Each Kubernetes Codex credential stream runs one job at a time."}</p>}
    {connected === false && <p className="dashboard-empty">GitHub is disconnected. Recorded work is still available. <a href="#/setup">Connection settings</a></p>}
    {view.kind === "error" ? <div role="alert" className="dashboard-error"><h2>Dashboard unavailable</h2><p>{view.message}</p><p>Reconnecting automatically.</p></div> : view.kind === "loading" ? <p role="status">Loading your workspace…</p> : <>
      {view.empty && <p className="dashboard-empty">{project ? "No work for this project. Choose another project or start a new run." : "No work yet. Start a new run to fill your queue."}</p>}
      <Queue title="Needs you" jobs={view.attention} empty="No decisions or blockers need your attention." />
      <Queue title="Working now" jobs={view.active} empty="No work is running or queued." />
      <Queue title="Ready to review" jobs={view.ready} empty="No work is waiting for review." />
      <Queue title="Recent outcomes" jobs={view.recent} empty="No recent outcomes for this project." />
    </>}
  </div>;
}

function Queue({ title, jobs, empty }) {
  return <section className="work-queue" aria-label={title}><h2>{title}</h2>
    {jobs.length ? <ul>{jobs.map(job => <li key={job.id} className="work-queue-row">
      <div className="work-queue-task"><small>{job.repository || "No project recorded"}</small><a href={jobURL(job)}>{jobDisplayTitle(job)}</a><p>{queueSummary(job)}</p></div>
      <State value={job.state} compact />
      <a className="queue-action" href={jobURL(job)}>{job.publication_hold ? "Review hold" : job.state === "awaiting_merge" ? "Review run" : job.state === "awaiting_approval" ? "Review approval" : dashboardAction(job)}</a>
    </li>)}</ul> : <p className="dashboard-empty">{empty}</p>}
  </section>;
}
const dashboardAction = job => ["blocked", "failed", "timed_out", "interrupted"].includes(job.state) ? "View blocker" : ["running", "queued"].includes(job.state) ? "View progress" : "View result";

export function ActivityPage({ jobs = [], workers = [], automations, loaded, error, now }) {
  const [days, setDays] = useState("7");
  const view = useMemo(() => dashboardState({ jobs, workers, automations, days, loaded, error, now }), [jobs, workers, automations, days, loaded, error, now]);
  return <div className="runs-page dashboard-page"><PageHeading title="Activity" description="Recorded job activity and resource usage." />
    {view.kind === "error" ? <div role="alert">Activity unavailable: {view.message}</div> : view.kind === "loading" ? <p role="status">Loading activity…</p> : <>
      <div className="dashboard-columns">
        <section className="dashboard-activity" aria-labelledby="activity-heading">
          <header className="dashboard-section-heading"><div><h2 id="activity-heading">Job activity</h2><p>Jobs created in the selected window.</p></div><label className="dashboard-window"><span className="sr-only">Time window</span><select aria-label="Time window" value={days} onChange={event => setDays(event.target.value)}><option value="7">Last 7 days</option><option value="30">Last 30 days</option></select></label></header>
          <div className="dashboard-activity-total"><strong>{view.metrics.totalTasks}</strong><span>job{view.metrics.totalTasks === 1 ? "" : "s"} created</span></div>
          <ActivityChart dates={view.dates} />
          <div className="dashboard-outcomes" aria-label="Current outcomes of jobs created in this window"><span><i className="outcome-success" />{view.counts.succeeded} succeeded</span><span><i className="outcome-failed" />{view.counts.failed} failed</span><span><i className="outcome-cancelled" />{view.counts.cancelled} cancelled</span><span><i className="outcome-pending" />{view.counts.unfinished} unfinished</span></div>
        </section>

        <section className="dashboard-runtime" aria-labelledby="runtime-heading"><header className="dashboard-section-heading"><div><h2 id="runtime-heading">Workspace</h2><p>Runners and recurring work.</p></div></header>
          <a className="dashboard-runtime-link" href="#/runners"><Server className="size-4" /><span><strong>{view.onlineWorkers} runner{view.onlineWorkers === 1 ? "" : "s"} online</strong><small>{view.totalWorkers ? `${view.totalWorkers} registered` : "Connect a runner to start work"}</small></span><ArrowRight className="size-3.5" /></a>
          <a className="dashboard-runtime-link" href="#/automations"><Clock3 className="size-4" /><span><strong>{view.scheduled === null ? "Automations" : `${view.scheduled} active schedule${view.scheduled === 1 ? "" : "s"}`}</strong><small>{view.automationCount === null ? "View configured triggers" : `${view.automationCount} saved automation${view.automationCount === 1 ? "" : "s"}`}</small></span><ArrowRight className="size-3.5" /></a>
          <div className="dashboard-usage"><dl><div><dt>Timed agent execution</dt><dd>{view.timedMillis === null ? "Not recorded" : formatDurationMillis(Math.round(view.timedMillis / 1000) * 1000)}</dd></div><div><dt>Reported tokens</dt><dd>{view.usage.total === undefined ? "Not reported" : formatTokenUsage(view.usage.total)}</dd></div><div><dt>Estimated cost</dt><dd>{formatEstimatedCost(view.cost.total)}</dd></div></dl><p>For jobs created in this window. Timing: {view.timedRuns}/{view.runs.length} completed attempts. Usage: {view.usage.reported}/{view.usage.completed}. Estimated cost (USD): {view.cost.estimated}/{view.metrics.totalTasks} jobs. Blended token estimates, not billed charges. Jobs with unknown models or missing usage are excluded.</p></div>
        </section>
      </div>
      <section className="work-queue" aria-label="Estimated cost per job"><h2>Estimated cost per job</h2>
        {view.metrics.tasks.length ? <ul>{view.metrics.tasks.map(job => <li key={job.id} className="work-queue-row activity-cost-row">
          <div className="work-queue-task"><small>{job.repository || "No project recorded"}</small><a href={jobURL(job)}>{jobDisplayTitle(job)}</a></div>
          <span>{formatEstimatedCost(estimatedJobCost(job))}</span>
        </li>)}</ul> : <p className="dashboard-empty">No jobs created in this window.</p>}
      </section>
    </>}
  </div>;
}

export function ActivityChart({ dates }) {
  const peak = Math.max(0, ...dates.map(day => day.count));
  const max = Math.max(1, peak);
  const step = 600 / dates.length;
  const label = date => date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return <div className="dashboard-chart"><svg viewBox="0 0 600 130" role="img" aria-label={`Jobs created per day, ${label(dates[0].date)} to ${label(dates.at(-1).date)}`} preserveAspectRatio="none"><line x1="0" y1="20" x2="600" y2="20" className="dashboard-chart-guide" /><line x1="0" y1="75" x2="600" y2="75" className="dashboard-chart-guide" /><line x1="0" y1="129" x2="600" y2="129" className="dashboard-chart-baseline" />{dates.map((day, i) => <rect key={day.key} x={i * step + step * .2} y={129 - day.count / max * 109} width={step * .6} height={day.count / max * 109} rx="2"><title>{`${label(day.date)}: ${day.count} job${day.count === 1 ? "" : "s"}`}</title></rect>)}</svg><div className="dashboard-chart-labels"><span>{label(dates[0].date)}</span><span>{peak} job{peak === 1 ? "" : "s"} at peak</span><span>{label(dates.at(-1).date)}</span></div><p className="sr-only">{dates.map(day => `${label(day.date)}: ${day.count}`).join("; ")}</p></div>;
}
