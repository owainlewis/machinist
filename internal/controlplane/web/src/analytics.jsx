import { useMemo, useState } from "react";
import { ArrowRight, Clock3, Plus, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PageHeading } from "@/components/ui/page-heading";
import { dashboardState } from "./dashboard-state.js";
import { jobDisplayTitle } from "./runs-board.js";
import { StageProgress } from "./stage-progress.jsx";
import { State, relativeTime } from "./task-display.jsx";
import { formatDurationMillis, formatTokenUsage } from "./run-metrics.js";

const jobURL = job => `#/runs/${encodeURIComponent(job.id)}`;
export function Dashboard({ jobs = [], workers = [], automations, loaded, error, onNewRun, now }) {
  const [days, setDays] = useState("7");
  const view = useMemo(() => dashboardState({ jobs, workers, automations, days, loaded, error, now }), [jobs, workers, automations, days, loaded, error, now]);
  return <div className="runs-page dashboard-page">
    <PageHeading title="Dashboard" description="A clear view of your agents and their work.">
      <Button onClick={onNewRun}><Plus className="size-4" />New run</Button>
    </PageHeading>
    {view.kind === "error" ? <div role="alert" className="dashboard-error"><h2>Dashboard unavailable</h2><p>{view.message}</p><p>Reconnecting automatically. Work and metrics will return when the connection is restored.</p></div> : view.kind === "loading" ? <p role="status" className="dashboard-empty">Loading your workspace…</p> : <>
      <section className="dashboard-overview" aria-label="Current workload, all time">
        <Overview label="Active jobs" value={view.active.length} detail={`${view.running} running · ${view.queued} queued`} href="#/runs?filter=active" tone="primary" />
        <Overview label="PRs ready" value={view.ready.length} detail="Waiting for your review and merge" href="#/runs?filter=pr_ready" tone="info" />
        <Overview label="Needs attention" value={view.attention.length} detail="Approvals, blockers and failures" href="#/runs?filter=attention" tone="warning" />
      </section>

      {jobs.length === 0 && <section className="dashboard-welcome"><h2>Start with one useful job</h2><p>Give an agent a task, connect an issue, or save a recurring automation. Its progress and results will appear here.</p><div><Button variant="outline" onClick={onNewRun}>Create your first run<ArrowRight className="size-3.5" /></Button><a href="#/automations">Set up an automation</a></div></section>}

      {(view.active.length > 0 || view.ready.length > 0 || view.attention.length > 0) && <section className="dashboard-priorities" aria-labelledby="priority-heading"><header className="dashboard-section-heading"><div><h2 id="priority-heading">Keep work moving</h2><p>Current work across all dates.</p></div><a href="#/runs">View runs<ArrowRight className="size-3.5" /></a></header><div className="dashboard-priority-columns">
        {view.active.length > 0 && <Priority title="In progress" jobs={view.active} href="#/runs?filter=active" />}
        {view.ready.length > 0 && <Priority title="Ready to merge" jobs={view.ready} href="#/runs?filter=pr_ready" />}
        {view.attention.length > 0 && <Priority title="Needs attention" jobs={view.attention} href="#/runs?filter=attention" />}
      </div></section>}


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
          <div className="dashboard-usage"><dl><div><dt>Timed agent execution</dt><dd>{view.timedMillis === null ? "Not recorded" : formatDurationMillis(Math.round(view.timedMillis / 1000) * 1000)}</dd></div><div><dt>Reported tokens</dt><dd>{view.usage.total === undefined ? "Not reported" : formatTokenUsage(view.usage.total)}</dd></div></dl><p>For jobs created in this window. Timing: {view.timedRuns}/{view.runs.length} completed attempts. Usage: {view.usage.reported}/{view.usage.completed}. No cost estimates.</p></div>
        </section>
      </div>

      <section className="dashboard-recent" aria-labelledby="recent-heading"><header className="dashboard-section-heading"><div><h2 id="recent-heading">Recent jobs</h2><p>Latest updates to jobs created in this window.</p></div><a href="#/runs">All runs<ArrowRight className="size-3.5" /></a></header>
        {view.recent.length ? <div className="dashboard-job-list">{view.recent.map(job => <div key={job.id} className="dashboard-job-row"><a href={jobURL(job)} className="dashboard-job-title"><strong>{jobDisplayTitle(job)}</strong><span>{job.repository || "No repository"}</span></a><div className="dashboard-job-stage"><StageProgress job={job} /></div><State value={job.state} compact /><time dateTime={job.updated_at || job.created_at}>{relativeTime(job.updated_at || job.created_at)}</time></div>)}</div> : <p className="dashboard-empty">No jobs created in the last {days} days. Choose a wider window or start a new run.</p>}
      </section>
    </>}
  </div>;
}

function Overview({ label, value, detail, href, tone }) {
  return <a className={`dashboard-stat dashboard-stat-${tone}`} href={href}><div><span>{label}</span><ArrowRight className="size-3.5" /></div><strong>{value}</strong><small>{detail}</small></a>;
}
function Priority({ title, jobs, href }) {
  return <div className="dashboard-priority"><h3>{title}<span>{jobs.length}</span></h3><>{jobs.slice(0, 3).map(job => <a key={job.id} href={jobURL(job)}><strong>{jobDisplayTitle(job)}</strong><small>{job.repository}</small><State value={job.state} compact /></a>)}{jobs.length > 3 && <a className="dashboard-see-all" href={href}>View all {jobs.length}<ArrowRight className="size-3.5" /></a>}</></div>;
}
export function ActivityChart({ dates }) {
  const peak = Math.max(0, ...dates.map(day => day.count));
  const max = Math.max(1, peak);
  const step = 600 / dates.length;
  const label = date => date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return <div className="dashboard-chart"><svg viewBox="0 0 600 130" role="img" aria-label={`Jobs created per day, ${label(dates[0].date)} to ${label(dates.at(-1).date)}`} preserveAspectRatio="none"><line x1="0" y1="20" x2="600" y2="20" className="dashboard-chart-guide" /><line x1="0" y1="75" x2="600" y2="75" className="dashboard-chart-guide" /><line x1="0" y1="129" x2="600" y2="129" className="dashboard-chart-baseline" />{dates.map((day, i) => <rect key={day.key} x={i * step + step * .2} y={129 - day.count / max * 109} width={step * .6} height={day.count / max * 109} rx="2"><title>{`${label(day.date)}: ${day.count} job${day.count === 1 ? "" : "s"}`}</title></rect>)}</svg><div className="dashboard-chart-labels"><span>{label(dates[0].date)}</span><span>{peak} job{peak === 1 ? "" : "s"} at peak</span><span>{label(dates.at(-1).date)}</span></div><p className="sr-only">{dates.map(day => `${label(day.date)}: ${day.count}`).join("; ")}</p></div>;
}
